package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/teambition/rrule-go"
)

type Config struct {
	Template Template `json:"template"`
	Schedule Schedule `json:"schedule"`
}
type Template struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	MaxPlayers  int      `json:"max_players"`
	Entry       Entry    `json:"entry"`
	Rounds      []Round  `json:"rounds"`
	Rewards     []Reward `json:"rewards"`
	TiePolicy   string   `json:"tie_policy"`
}
type Entry struct {
	Type     string `json:"type"`
	TicketID string `json:"ticket_id"`
}
type Round struct {
	Number          int        `json:"number"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	DurationMinutes int        `json:"duration_minutes"`
	Rules           RoundRules `json:"rules"`
}
type RoundRules struct {
	AttemptLimit     int    `json:"attempt_limit"`
	ScoreAggregation string `json:"score_aggregation"`
}
type Reward struct {
	Place  int    `json:"place"`
	Type   string `json:"type"`
	Amount int    `json:"amount"`
}
type Schedule struct {
	ID                             string `json:"id"`
	TemplateID                     string `json:"template_id"`
	DTSTART                        string `json:"dtstart"`
	Timezone                       string `json:"timezone"`
	RRULE                          string `json:"rrule"`
	RegistrationOpenOffsetSeconds  int    `json:"registration_open_offset_seconds"`
	RegistrationCloseOffsetSeconds int    `json:"registration_close_offset_seconds"`
	Enabled                        bool   `json:"enabled"`
}
type Tournament struct {
	ID                   string   `json:"id"`
	TemplateID           string   `json:"template_id"`
	ScheduleID           string   `json:"schedule_id"`
	StartsAt             string   `json:"starts_at"`
	RegistrationOpensAt  string   `json:"registration_opens_at"`
	RegistrationClosesAt string   `json:"registration_closes_at"`
	Status               string   `json:"status"`
	TemplateSnapshot     Template `json:"template_snapshot"`
	ScheduleSnapshot     Schedule `json:"schedule_snapshot"`
}
type State struct {
	Config      Config       `json:"config"`
	Tournaments []Tournament `json:"tournaments"`
}
type Occurrence struct {
	StartsAt             string `json:"starts_at"`
	RegistrationOpensAt  string `json:"registration_opens_at"`
	RegistrationClosesAt string `json:"registration_closes_at"`
}

//go:embed schemas/template.json
var templateSchemaJSON []byte

//go:embed schemas/schedule.json
var scheduleSchemaJSON []byte

//go:embed schemas/tournament.json
var persistedTournamentSchemaJSON []byte

var configSchema *jsonschema.Resolved
var persistedTournamentSchema *jsonschema.Resolved

func init() {
	var templateSchema, scheduleSchema any
	if err := json.Unmarshal(templateSchemaJSON, &templateSchema); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(scheduleSchemaJSON, &scheduleSchema); err != nil {
		panic(err)
	}
	configSchemaJSON, err := json.Marshal(map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"template", "schedule"},
		"properties": map[string]any{
			"template": map[string]string{"$ref": "#/$defs/template"},
			"schedule": map[string]string{"$ref": "#/$defs/schedule"},
		},
		"$defs": map[string]any{
			"template": templateSchema,
			"schedule": scheduleSchema,
		},
	})
	if err != nil {
		panic(err)
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(configSchemaJSON, &schema); err != nil {
		panic(err)
	}
	configSchema, err = schema.Resolve(nil)
	if err != nil {
		panic(err)
	}
	var persisted jsonschema.Schema
	if err = json.Unmarshal(persistedTournamentSchemaJSON, &persisted); err != nil {
		panic(err)
	}
	persistedTournamentSchema, err = persisted.Resolve(nil)
	if err != nil {
		panic(err)
	}
}

func (c *Config) UnmarshalJSON(data []byte) error {
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return err
	}
	if err := configSchema.Validate(instance); err != nil {
		return fmt.Errorf("config schema: %w", err)
	}
	type configAlias Config
	var decoded configAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
}

func (t *Tournament) UnmarshalJSON(data []byte) error {
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return err
	}
	if err := persistedTournamentSchema.Validate(instance); err != nil {
		return fmt.Errorf("tournament schema: %w", err)
	}
	type tournamentAlias Tournament
	var decoded tournamentAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = Tournament(decoded)
	return nil
}

func validateTournament(t Tournament) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var instance any
	if err = json.Unmarshal(b, &instance); err != nil {
		return err
	}
	if err = persistedTournamentSchema.Validate(instance); err != nil {
		return fmt.Errorf("tournament schema: %w", err)
	}
	return nil
}
func validateConfig(c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var instance any
	if err = json.Unmarshal(b, &instance); err != nil {
		return err
	}
	if err = configSchema.Validate(instance); err != nil {
		return fmt.Errorf("config schema: %w", err)
	}
	if c.Template.Entry.Type == "free" && c.Template.Entry.TicketID != "" {
		return errors.New("free entry must not specify ticket_id")
	}
	if c.Template.Entry.Type == "ticket" && strings.TrimSpace(c.Template.Entry.TicketID) == "" {
		return errors.New("ticket entry requires ticket_id")
	}
	if c.Template.TiePolicy != "shared_place_full_reward" {
		return errors.New("unsupported tie policy")
	}
	for i, r := range c.Template.Rounds {
		if r.Number != i+1 {
			return errors.New("round numbers must be sequential starting at 1")
		}
	}
	seen := make(map[int]bool, len(c.Template.Rewards))
	for _, reward := range c.Template.Rewards {
		if reward.Place > c.Template.MaxPlayers {
			return errors.New("reward place must not exceed max_players")
		}
		if seen[reward.Place] {
			return errors.New("reward places must be unique")
		}
		seen[reward.Place] = true
	}
	if c.Schedule.TemplateID != c.Template.ID {
		return errors.New("schedule template_id must match template id")
	}
	if c.Schedule.RegistrationOpenOffsetSeconds > c.Schedule.RegistrationCloseOffsetSeconds || c.Schedule.RegistrationCloseOffsetSeconds > 0 {
		return errors.New("registration offsets must satisfy open <= close <= 0 seconds")
	}
	_, err = recurrence(c.Schedule)
	return err
}

func defaultConfig() Config {
	return Config{Template: Template{ID: "autumn-quiz", Name: "Осенний турнир знаний", Description: "Ежедневная викторина с накоплением баллов.", MaxPlayers: 128, Entry: Entry{Type: "free", TicketID: ""}, Rounds: []Round{{Number: 1, Name: "Разминка", Description: "Пять коротких вопросов.", DurationMinutes: 10, Rules: RoundRules{AttemptLimit: 1, ScoreAggregation: "best"}}, {Number: 2, Name: "Финал", Description: "Вопросы повышенной сложности.", DurationMinutes: 15, Rules: RoundRules{AttemptLimit: 2, ScoreAggregation: "sum"}}}, Rewards: []Reward{{Place: 1, Type: "points", Amount: 100}, {Place: 2, Type: "points", Amount: 60}, {Place: 3, Type: "points", Amount: 30}}, TiePolicy: "shared_place_full_reward"}, Schedule: Schedule{ID: "weekday-evening", TemplateID: "autumn-quiz", DTSTART: "2026-10-19T20:00:00", Timezone: "Europe/Moscow", RRULE: "FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,TU,WE,TH,FR;COUNT=10", RegistrationOpenOffsetSeconds: -86400, RegistrationCloseOffsetSeconds: -3600, Enabled: true}}
}

func recurrence(s Schedule) (*rrule.RRule, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone: %w", err)
	}
	start, err := time.ParseInLocation("2006-01-02T15:04:05", s.DTSTART, loc)
	if err != nil || start.Format("2006-01-02T15:04:05") != s.DTSTART {
		return nil, errors.New("dtstart must be a valid local ISO datetime without offset")
	}
	parts := strings.Split(s.RRULE, ";")
	fields := make(map[string]string, len(parts))
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			return nil, errors.New("invalid RRULE component")
		}
		if _, ok := fields[kv[0]]; ok {
			return nil, fmt.Errorf("duplicate RRULE part %s", kv[0])
		}
		fields[kv[0]] = kv[1]
	}
	for k := range fields {
		if k != "FREQ" && k != "INTERVAL" && k != "BYDAY" && k != "COUNT" && k != "UNTIL" {
			return nil, fmt.Errorf("unsupported RRULE part %s", k)
		}
	}
	freq := fields["FREQ"]
	if freq != "DAILY" && freq != "WEEKLY" {
		return nil, errors.New("RRULE FREQ must be DAILY or WEEKLY")
	}
	if _, a := fields["COUNT"]; a {
		if _, b := fields["UNTIL"]; b {
			return nil, errors.New("RRULE cannot combine COUNT and UNTIL")
		}
	}
	if _, a := fields["COUNT"]; !a {
		if _, b := fields["UNTIL"]; !b {
			return nil, errors.New("RRULE requires COUNT or UNTIL")
		}
	}
	interval := 1
	if v, ok := fields["INTERVAL"]; ok {
		interval, err = strconv.Atoi(v)
		if err != nil || interval < 1 || interval > 10000 {
			return nil, errors.New("RRULE INTERVAL must be between 1 and 10000")
		}
	}
	opt := rrule.ROption{Dtstart: start, Interval: interval}
	if freq == "DAILY" {
		opt.Freq = rrule.DAILY
	} else {
		opt.Freq = rrule.WEEKLY
	}
	if v, ok := fields["COUNT"]; ok {
		opt.Count, err = strconv.Atoi(v)
		if err != nil || opt.Count < 1 || opt.Count > 10000 {
			return nil, errors.New("RRULE COUNT must be between 1 and 10000")
		}
	}
	if v, ok := fields["UNTIL"]; ok {
		opt.Until, err = time.Parse("20060102T150405Z", v)
		if err != nil {
			return nil, errors.New("RRULE UNTIL must be UTC YYYYMMDDTHHMMSSZ")
		}
	}
	if v, ok := fields["BYDAY"]; ok {
		if freq != "WEEKLY" {
			return nil, errors.New("BYDAY is supported only for WEEKLY")
		}
		names := strings.Split(v, ",")
		seen := map[string]bool{}
		days := make([]rrule.Weekday, 0, len(names))
		dayMap := map[string]rrule.Weekday{"MO": rrule.MO, "TU": rrule.TU, "WE": rrule.WE, "TH": rrule.TH, "FR": rrule.FR, "SA": rrule.SA, "SU": rrule.SU}
		for _, n := range names {
			d, ok := dayMap[n]
			if !ok || seen[n] {
				return nil, errors.New("BYDAY must contain unique weekday names MO..SU without ordinals")
			}
			seen[n] = true
			days = append(days, d)
		}
		opt.Byweekday = days
	}
	r, err := rrule.NewRRule(opt)
	if err != nil {
		return nil, err
	}
	next := r.Iterator()
	first, ok := next()
	if !ok || !first.Equal(start) {
		return nil, errors.New("dtstart does not belong to RRULE")
	}
	if _, finiteByUntil := fields["UNTIL"]; finiteByUntil {
		occurrences := 1
		for occurrences <= 10000 {
			_, ok := next()
			if !ok {
				break
			}
			occurrences++
		}
		if occurrences > 10000 {
			return nil, errors.New("RRULE exceeds the 10000 occurrence limit")
		}
	}
	return r, nil
}

func addSeconds(t time.Time, seconds int) (time.Time, error) {
	offset := int64(seconds)
	unix := t.Unix()
	if offset > 0 && unix > (1<<63-1)-offset || offset < 0 && unix < (-1<<63)-offset {
		return time.Time{}, errors.New("registration offset is outside the supported timestamp range")
	}
	result := time.Unix(unix+offset, int64(t.Nanosecond())).In(t.Location())
	if result.Year() < 0 || result.Year() > 9999 {
		return time.Time{}, errors.New("registration offset is outside the supported timestamp range")
	}
	return result, nil
}

func makeOccurrence(start time.Time, s Schedule, loc *time.Location) (Occurrence, error) {
	local := start.In(loc)
	open, err := addSeconds(local, s.RegistrationOpenOffsetSeconds)
	if err != nil {
		return Occurrence{}, err
	}
	close, err := addSeconds(local, s.RegistrationCloseOffsetSeconds)
	if err != nil {
		return Occurrence{}, err
	}
	return Occurrence{formatOccurrenceTime(local), formatOccurrenceTime(open), formatOccurrenceTime(close)}, nil
}

func formatOccurrenceTime(t time.Time) string {
	_, offset := t.Zone()
	if offset%60 != 0 {
		return t.UTC().Format(time.RFC3339)
	}
	return t.Format(time.RFC3339)
}

func scheduleOccurrences(s Schedule, count int) ([]Occurrence, error) {
	if count < 1 || count > 50 {
		return nil, errors.New("count must be between 1 and 50")
	}
	r, err := recurrence(s)
	if err != nil {
		return nil, err
	}
	loc, _ := time.LoadLocation(s.Timezone)
	next := r.Iterator()
	out := make([]Occurrence, 0, count)
	for steps := 0; steps < 10000 && len(out) < count; steps++ {
		start, ok := next()
		if !ok {
			break
		}
		item, err := makeOccurrence(start, s, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func selectedOccurrence(s Schedule, value string) (Occurrence, error) {
	target, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return Occurrence{}, errors.New("starts_at must be RFC3339")
	}
	r, err := recurrence(s)
	if err != nil {
		return Occurrence{}, err
	}
	loc, _ := time.LoadLocation(s.Timezone)
	target = target.In(loc)
	next := r.Iterator()
	for steps := 0; steps < 10000; steps++ {
		start, ok := next()
		if !ok || start.After(target) {
			break
		}
		if start.Equal(target) {
			return makeOccurrence(start, s, loc)
		}
	}
	return Occurrence{}, errors.New("selected starts_at is not an occurrence in this recurrence")
}

func decodeStrict(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
