package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// MemoryExtractionInstruction is the complete contract for the auxiliary
	// completion that extracts durable memory candidates from one turn.
	MemoryExtractionInstruction = `Извлеки только устойчивые обновления памяти из этого turn. Верни ровно один JSON document без Markdown, пояснений или дополнительного текста.

Допустимая форма:
{"profile":{"language":"...","response_style":"...","response_format":"...","constraints":"..."},"task":{"goal":"...","plan":"...","current_step":"...","expected_action":"..."},"records":[{"scope":"profile","kind":"decision","key":"...","value":"..."}]}

Корневые разделы необязательны, но каждый присутствующий profile, task или records обязан быть непустым; {} — единственный ответ без обновлений. profile может содержать только language, response_style, response_format и constraints. task может содержать только goal, plan, current_step и expected_action. Каждый record обязан содержать ровно scope profile или task, kind decision или knowledge, и непустые string key/value. Снаружи строк убираются пробелы; key приводится к lowercase, внутренние пробелы заменяются на -, и затем обязан состоять только из a-z, 0-9, ., _ или -. Не повторяй records с одинаковым нормализованным (scope, kind, key). Не выбирай профиль, не меняй task phase или паузу, не создавай и не меняй invariants.`
)

// MemoryScope identifies the active owner to which a long-term candidate will
// be routed. It never identifies a profile or task by ID; routing always uses
// the currently active owner.
type MemoryScope string

const (
	MemoryScopeProfile MemoryScope = "profile"
	MemoryScopeTask    MemoryScope = "task"
)

// ProfileUpdate contains only allowlisted active-profile preference candidates.
// A nil field means that field was not supplied. Treat values returned by the
// parser as immutable by convention.
type ProfileUpdate struct {
	Language       *string
	ResponseStyle  *string
	ResponseFormat *string
	Constraints    *string
}

// TaskUpdate contains allowlisted active-task working-memory candidates. An
// empty field was not supplied and must be preserved by later routing. The
// extractor deliberately cannot alter lifecycle phase, pause state, or active
// selection. Treat values returned by the parser as immutable by convention.
type TaskUpdate struct {
	Goal           string
	Plan           string
	CurrentStep    string
	ExpectedAction string
}

// LongTermMemoryUpdate is one decision or knowledge candidate for the current
// active profile or task. Treat values returned by the parser as immutable by
// convention.
type LongTermMemoryUpdate struct {
	Scope MemoryScope
	Kind  MemoryKind
	Key   string
	Value string
}

// MemoryUpdate is a wholly validated collection of extractor candidates. It
// holds no persistent IDs and cannot select a profile, create a task, or mutate
// lifecycle state. Treat it and its nested values as immutable by convention.
type MemoryUpdate struct {
	Profile *ProfileUpdate
	Task    *TaskUpdate
	Records []LongTermMemoryUpdate
}

// MemoryExtractionState is the relevant active state supplied to the
// auxiliary completion. It is read-only input; BuildMemoryExtractionPrompt
// never mutates it or its pointed-to values.
type MemoryExtractionState struct {
	Profile    *Profile
	Task       *Task
	Invariants []Invariant
}

// BuildMemoryExtractionPrompt builds the deterministic auxiliary prompt for
// one completed turn. It returns a new message slice on every call and makes
// absent active owners explicit so the extractor cannot invent their updates.
func BuildMemoryExtractionPrompt(input, response string, state MemoryExtractionState) []CompletionMessage {
	return []CompletionMessage{
		{Role: "system", Content: MemoryExtractionInstruction},
		{Role: "system", Content: MemoryExtractionStateBlock(state)},
		{Role: "user", Content: "Пользователь сообщил:\n" + input},
		{Role: "assistant", Content: "Ассистент ответил:\n" + response},
	}
}

// MemoryExtractionStateBlock serializes active extraction state in a stable
// order. It does not expose any way for the extractor to change that state.
func MemoryExtractionStateBlock(state MemoryExtractionState) string {
	var block strings.Builder
	if state.Profile == nil {
		block.WriteString("Активный профиль отсутствует: не предлагай profile или records со scope profile.")
	} else {
		fmt.Fprintf(&block, "Активный профиль (обновлять можно только его):\nname: %s\nlanguage: %s\nresponse_style: %s\nresponse_format: %s\nconstraints: %s", state.Profile.Name, state.Profile.Language, state.Profile.ResponseStyle, state.Profile.ResponseFormat, state.Profile.Constraints)
	}
	block.WriteString("\n\n")

	if state.Task == nil {
		block.WriteString("Активная задача отсутствует: не предлагай task или records со scope task.")
	} else {
		fmt.Fprintf(&block, "Активная задача (не меняй phase или paused):\ngoal: %s\nplan: %s\nphase: %s\ncurrent_step: %s\nexpected_action: %s\npaused: %t", state.Task.Goal, state.Task.Plan, state.Task.Phase, state.Task.CurrentStep, state.Task.ExpectedAction, state.Task.Paused)
	}
	block.WriteString("\n\nАктивные invariants:")
	if len(state.Invariants) == 0 {
		block.WriteString("\nнет")
		return block.String()
	}

	invariants := append([]Invariant(nil), state.Invariants...)
	sort.Slice(invariants, func(i, j int) bool {
		if invariants[i].Category != invariants[j].Category {
			return invariants[i].Category < invariants[j].Category
		}
		if invariants[i].Key != invariants[j].Key {
			return invariants[i].Key < invariants[j].Key
		}
		return invariants[i].ID < invariants[j].ID
	})
	for _, invariant := range invariants {
		fmt.Fprintf(&block, "\n%s.%s = %s; rule: %s; source: %s", invariant.Category, invariant.Key, invariant.RequiredValue, invariant.Rule, invariant.Source)
		if invariant.Forbidden != "" {
			fmt.Fprintf(&block, "; forbidden: %s", invariant.Forbidden)
		}
	}
	return block.String()
}

// ParseMemoryUpdateJSON accepts exactly one strict JSON document and returns a
// wholly new validated update. It never mutates caller-owned state.
func ParseMemoryUpdateJSON(content string) (MemoryUpdate, error) {
	value, err := decodeStrictJSON(content)
	if err != nil {
		return MemoryUpdate{}, err
	}

	root, ok := value.(map[string]any)
	if !ok {
		return MemoryUpdate{}, fmt.Errorf("memory extractor response must be an object")
	}

	update := MemoryUpdate{}
	for field, raw := range root {
		switch field {
		case "profile":
			if update.Profile != nil {
				return MemoryUpdate{}, fmt.Errorf("duplicate profile update")
			}
			profile, err := parseProfileUpdate(raw)
			if err != nil {
				return MemoryUpdate{}, err
			}
			update.Profile = &profile
		case "task":
			if update.Task != nil {
				return MemoryUpdate{}, fmt.Errorf("duplicate task update")
			}
			task, err := parseTaskUpdate(raw)
			if err != nil {
				return MemoryUpdate{}, err
			}
			update.Task = &task
		case "records":
			if update.Records != nil {
				return MemoryUpdate{}, fmt.Errorf("duplicate records update")
			}
			records, err := parseLongTermMemoryUpdates(raw)
			if err != nil {
				return MemoryUpdate{}, err
			}
			update.Records = records
		default:
			return MemoryUpdate{}, fmt.Errorf("unknown memory extractor field %q", field)
		}
	}

	return cloneMemoryUpdate(update), nil
}

func parseProfileUpdate(raw any) (ProfileUpdate, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return ProfileUpdate{}, fmt.Errorf("profile update must be an object")
	}
	if len(object) == 0 {
		return ProfileUpdate{}, fmt.Errorf("profile update is empty")
	}

	var update ProfileUpdate
	for field, rawValue := range object {
		value, err := requiredMemoryString("profile "+field, rawValue)
		if err != nil {
			return ProfileUpdate{}, err
		}
		valueCopy := value
		switch field {
		case "language":
			update.Language = &valueCopy
		case "response_style":
			update.ResponseStyle = &valueCopy
		case "response_format":
			update.ResponseFormat = &valueCopy
		case "constraints":
			update.Constraints = &valueCopy
		default:
			return ProfileUpdate{}, fmt.Errorf("unknown profile update field %q", field)
		}
	}
	return update, nil
}

func parseTaskUpdate(raw any) (TaskUpdate, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return TaskUpdate{}, fmt.Errorf("task update must be an object")
	}
	if len(object) == 0 {
		return TaskUpdate{}, fmt.Errorf("task update is empty")
	}

	var update TaskUpdate
	for field, rawValue := range object {
		value, err := requiredMemoryString("task "+field, rawValue)
		if err != nil {
			return TaskUpdate{}, err
		}
		switch field {
		case "goal":
			update.Goal = value
		case "plan":
			update.Plan = value
		case "current_step":
			update.CurrentStep = value
		case "expected_action":
			update.ExpectedAction = value
		default:
			return TaskUpdate{}, fmt.Errorf("unknown task update field %q", field)
		}
	}
	return update, nil
}

func parseLongTermMemoryUpdates(raw any) ([]LongTermMemoryUpdate, error) {
	array, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("records update must be an array")
	}
	if len(array) == 0 {
		return nil, fmt.Errorf("records update is empty")
	}

	records := make([]LongTermMemoryUpdate, 0, len(array))
	seen := make(map[string]struct{}, len(array))
	for index, rawRecord := range array {
		record, err := parseLongTermMemoryUpdate(rawRecord)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", index, err)
		}
		identity := string(record.Scope) + "\x00" + string(record.Kind) + "\x00" + record.Key
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("record %d duplicates a normalized candidate", index)
		}
		seen[identity] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

func parseLongTermMemoryUpdate(raw any) (LongTermMemoryUpdate, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return LongTermMemoryUpdate{}, fmt.Errorf("must be an object")
	}
	if len(object) != 4 {
		return LongTermMemoryUpdate{}, fmt.Errorf("must contain exactly scope, kind, key, and value")
	}

	var record LongTermMemoryUpdate
	for field, rawValue := range object {
		value, err := requiredMemoryString("record "+field, rawValue)
		if err != nil {
			return LongTermMemoryUpdate{}, err
		}
		switch field {
		case "scope":
			record.Scope = MemoryScope(value)
		case "kind":
			record.Kind = MemoryKind(value)
		case "key":
			key, err := normalizeMemoryKey(value)
			if err != nil {
				return LongTermMemoryUpdate{}, err
			}
			record.Key = key
		case "value":
			record.Value = value
		default:
			return LongTermMemoryUpdate{}, fmt.Errorf("unknown record field %q", field)
		}
	}
	if record.Scope != MemoryScopeProfile && record.Scope != MemoryScopeTask {
		return LongTermMemoryUpdate{}, fmt.Errorf("unknown record scope %q", record.Scope)
	}
	if record.Kind != MemoryKindDecision && record.Kind != MemoryKindKnowledge {
		return LongTermMemoryUpdate{}, fmt.Errorf("unknown record kind %q for scope %q", record.Kind, record.Scope)
	}
	if record.Key == "" || record.Value == "" {
		return LongTermMemoryUpdate{}, fmt.Errorf("record must contain exactly scope, kind, key, and value")
	}
	return record, nil
}

func requiredMemoryString(name string, raw any) (string, error) {
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	return value, nil
}

func normalizeMemoryKey(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), "-")
	if value == "" {
		return "", fmt.Errorf("record key is empty")
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return "", fmt.Errorf("record key must normalize to an identifier")
		}
	}
	return value, nil
}

func decodeStrictJSON(content string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("decode memory extractor response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("memory extractor response contains trailing JSON")
		}
		return nil, fmt.Errorf("decode memory extractor response: %w", err)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate JSON object field %q", key)
				}
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("object is not closed")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("array is not closed")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", token)
		}
	case string, bool, nil, json.Number:
		return token, nil
	default:
		return nil, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func cloneMemoryUpdate(update MemoryUpdate) MemoryUpdate {
	clone := MemoryUpdate{}
	if update.Profile != nil {
		profile := ProfileUpdate{}
		if update.Profile.Language != nil {
			value := *update.Profile.Language
			profile.Language = &value
		}
		if update.Profile.ResponseStyle != nil {
			value := *update.Profile.ResponseStyle
			profile.ResponseStyle = &value
		}
		if update.Profile.ResponseFormat != nil {
			value := *update.Profile.ResponseFormat
			profile.ResponseFormat = &value
		}
		if update.Profile.Constraints != nil {
			value := *update.Profile.Constraints
			profile.Constraints = &value
		}
		clone.Profile = &profile
	}
	if update.Task != nil {
		task := *update.Task
		clone.Task = &task
	}
	if update.Records != nil {
		clone.Records = append([]LongTermMemoryUpdate(nil), update.Records...)
	}
	return clone
}
