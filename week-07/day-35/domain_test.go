package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTournamentUnmarshalRequiresPersistedFields(t *testing.T) {
	raw := `{"id":"demo","template_id":"autumn-quiz","schedule_id":"weekday-evening","starts_at":"2026-10-19T20:00:00+03:00","registration_opens_at":"2026-10-18T20:00:00+03:00","registration_closes_at":"2026-10-19T19:00:00+03:00","status":"scheduled","template_snapshot":{"id":"autumn-quiz","name":"Quiz","description":"","max_players":1,"entry":{"type":"free","ticket_id":""},"rounds":[{"number":1,"name":"Round","description":"","duration_minutes":1,"rules":{"attempt_limit":1,"score_aggregation":"best"}}],"rewards":[],"tie_policy":"shared_place_full_reward"},"schedule_snapshot":{"id":"weekday-evening","template_id":"autumn-quiz","dtstart":"2026-10-19T20:00:00","timezone":"Europe/Moscow","rrule":"FREQ=WEEKLY;COUNT=1","registration_close_offset_seconds":0,"enabled":true}}`
	var tournament Tournament
	if err := json.Unmarshal([]byte(raw), &tournament); err == nil {
		t.Fatal("accepted persisted tournament missing registration_open_offset_seconds")
	}
}

func TestConfigUnmarshalValidatesRawRequiredFieldsAndTypes(t *testing.T) {
	valid, err := json.Marshal(defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(valid, &config); err != nil {
		t.Fatal(err)
	}
	var schedule map[string]json.RawMessage
	if err := json.Unmarshal(config["schedule"], &schedule); err != nil {
		t.Fatal(err)
	}
	var template map[string]json.RawMessage
	if err := json.Unmarshal(config["template"], &template); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		change func(map[string]json.RawMessage, map[string]json.RawMessage)
	}{
		{name: "missing enabled", change: func(_, schedule map[string]json.RawMessage) { delete(schedule, "enabled") }},
		{name: "missing offset", change: func(_, schedule map[string]json.RawMessage) { delete(schedule, "registration_open_offset_seconds") }},
		{name: "missing description", change: func(template, _ map[string]json.RawMessage) { delete(template, "description") }},
		{name: "unknown field", change: func(_, schedule map[string]json.RawMessage) { schedule["unexpected"] = json.RawMessage(`true`) }},
		{name: "null enabled", change: func(_, schedule map[string]json.RawMessage) { schedule["enabled"] = json.RawMessage(`null`) }},
		{name: "wrong offset type", change: func(_, schedule map[string]json.RawMessage) {
			schedule["registration_close_offset_seconds"] = json.RawMessage(`null`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			currentTemplate := cloneRawObject(t, template)
			currentSchedule := cloneRawObject(t, schedule)
			tc.change(currentTemplate, currentSchedule)
			body, err := marshalConfigObjects(currentTemplate, currentSchedule)
			if err != nil {
				t.Fatal(err)
			}
			var got Config
			if err := json.Unmarshal(body, &got); err == nil {
				t.Fatal("accepted invalid raw config")
			}
		})
	}

	schedule["enabled"] = json.RawMessage(`false`)
	schedule["registration_open_offset_seconds"] = json.RawMessage(`0`)
	schedule["registration_close_offset_seconds"] = json.RawMessage(`0`)
	body, err := marshalConfigObjects(template, schedule)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("rejected explicit false and zero values: %v", err)
	}
	if got.Schedule.Enabled || got.Schedule.RegistrationOpenOffsetSeconds != 0 || got.Schedule.RegistrationCloseOffsetSeconds != 0 {
		t.Fatalf("false/zero values were not preserved: %+v", got.Schedule)
	}
}

func cloneRawObject(t *testing.T, input map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	output := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}

func marshalConfigObjects(template, schedule map[string]json.RawMessage) ([]byte, error) {
	return json.Marshal(map[string]any{"template": template, "schedule": schedule})
}
func TestWeeklyDatesAndSelectedOccurrenceMembership(t *testing.T) {
	c := defaultConfig()
	c.Schedule.DTSTART = "2026-10-19T20:00:00"
	c.Schedule.Timezone = "Europe/Moscow"
	c.Schedule.RRULE = "FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,WE;COUNT=3"
	items, err := scheduleOccurrences(c.Schedule, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-19T20:00:00+03:00", "2026-10-21T20:00:00+03:00", "2026-10-26T20:00:00+03:00"}
	if len(items) != len(want) {
		t.Fatalf("got %d occurrences, want %d", len(items), len(want))
	}
	for i := range want {
		if items[i].StartsAt != want[i] {
			t.Errorf("occurrence %d = %q, want %q", i, items[i].StartsAt, want[i])
		}
	}
	if _, err := selectedOccurrence(c.Schedule, "2026-10-20T20:00:00+03:00"); err == nil {
		t.Fatal("accepted non-matching weekday")
	}
}

func TestScheduleOccurrencesPreserveLocalTimeAcrossDST(t *testing.T) {
	c := defaultConfig()
	c.Schedule.Timezone = "Europe/Berlin"
	c.Schedule.DTSTART = "2026-10-24T20:00:00"
	c.Schedule.RRULE = "FREQ=DAILY;COUNT=3"
	items, err := scheduleOccurrences(c.Schedule, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-24T20:00:00+02:00", "2026-10-25T20:00:00+01:00", "2026-10-26T20:00:00+01:00"}
	for i := range want {
		if items[i].StartsAt != want[i] {
			t.Errorf("occurrence %d = %q, want %q", i, items[i].StartsAt, want[i])
		}
	}
}

func TestHistoricalSecondOffsetPreviewCanBeSelected(t *testing.T) {
	c := defaultConfig()
	c.Schedule.Timezone = "Europe/Paris"
	c.Schedule.DTSTART = "1900-01-01T12:00:00"
	c.Schedule.RRULE = "FREQ=DAILY;COUNT=1"
	c.Schedule.RegistrationOpenOffsetSeconds = 0
	c.Schedule.RegistrationCloseOffsetSeconds = 0

	preview, err := scheduleOccurrences(c.Schedule, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantInstant, err := time.Parse(time.RFC3339, "1900-01-01T11:50:39Z")
	if err != nil {
		t.Fatal(err)
	}
	previewInstant, err := time.Parse(time.RFC3339, preview[0].StartsAt)
	if err != nil {
		t.Fatalf("preview start %q is not RFC3339: %v", preview[0].StartsAt, err)
	}
	if !previewInstant.Equal(wantInstant) {
		t.Fatalf("preview instant = %s, want %s", previewInstant, wantInstant)
	}
	if preview[0].StartsAt != "1900-01-01T11:50:39Z" {
		t.Fatalf("preview start = %q, want UTC RFC3339", preview[0].StartsAt)
	}

	selected, err := selectedOccurrence(c.Schedule, preview[0].StartsAt)
	if err != nil {
		t.Fatalf("could not select preview occurrence: %v", err)
	}
	selectedInstant, err := time.Parse(time.RFC3339, selected.StartsAt)
	if err != nil {
		t.Fatalf("selected start %q is not RFC3339: %v", selected.StartsAt, err)
	}
	if !selectedInstant.Equal(wantInstant) {
		t.Fatalf("selected instant = %s, want %s", selectedInstant, wantInstant)
	}
}

func TestUntilOccurrenceLimitAndLastSupportedMembership(t *testing.T) {
	c := defaultConfig()
	c.Schedule.Timezone = "Etc/UTC"
	c.Schedule.DTSTART = "2000-01-01T00:00:00"
	c.Schedule.RRULE = "FREQ=DAILY;UNTIL=20280101T000000Z"
	if _, err := recurrence(c.Schedule); err == nil || !strings.Contains(err.Error(), "10000 occurrence limit") {
		t.Fatalf("oversized UNTIL recurrence error = %v, want 10000 occurrence limit", err)
	}

	c.Schedule.DTSTART = "2026-10-19T20:00:00"
	c.Schedule.RRULE = "FREQ=DAILY;UNTIL=20261021T200000Z"
	items, err := scheduleOccurrences(c.Schedule, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d occurrences, want 3", len(items))
	}
	last, err := selectedOccurrence(c.Schedule, items[len(items)-1].StartsAt)
	if err != nil {
		t.Fatalf("could not select final supported occurrence: %v", err)
	}
	if last.StartsAt != items[len(items)-1].StartsAt {
		t.Fatalf("selected start = %q, want final occurrence %q", last.StartsAt, items[len(items)-1].StartsAt)
	}
}

func TestRecurrenceRejectsUnsupportedOrInvalidRules(t *testing.T) {
	c := defaultConfig()
	for _, rule := range []string{"FREQ=MONTHLY;COUNT=2", "FREQ=DAILY;COUNT=2;BYDAY=MO", "FREQ=WEEKLY;COUNT=2;BYHOUR=10", "FREQ=DAILY;COUNT=2;COUNT=3", "FREQ=DAILY;INTERVAL=0;COUNT=2"} {
		t.Run(rule, func(t *testing.T) {
			c.Schedule.RRULE = rule
			if _, err := recurrence(c.Schedule); err == nil {
				t.Fatalf("recurrence accepted %q", rule)
			}
		})
	}
}

func TestRecurrenceRejectsNonexistentLocalStartAndForgedDate(t *testing.T) {
	c := defaultConfig()
	c.Schedule.Timezone = "Europe/Berlin"
	c.Schedule.DTSTART = "2026-03-29T02:30:00"
	c.Schedule.RRULE = "FREQ=DAILY;COUNT=2"
	if _, err := recurrence(c.Schedule); err == nil {
		t.Fatal("accepted nonexistent local DTSTART")
	}
	c.Schedule.DTSTART = "2026-03-28T20:00:00"
	if _, err := selectedOccurrence(c.Schedule, "2026-03-29T20:01:00+02:00"); err == nil || !strings.Contains(err.Error(), "not an occurrence") {
		t.Fatalf("forged date error = %v", err)
	}
}

func TestRegistrationOffsetsAreSignedSeconds(t *testing.T) {
	c := defaultConfig()
	c.Schedule.DTSTART = "2026-10-19T20:00:00"
	c.Schedule.Timezone = "Europe/Moscow"
	c.Schedule.RRULE = "FREQ=DAILY;COUNT=1"
	c.Schedule.RegistrationOpenOffsetSeconds = -3599
	c.Schedule.RegistrationCloseOffsetSeconds = -1
	items, err := scheduleOccurrences(c.Schedule, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := items[0].RegistrationOpensAt, "2026-10-19T19:00:01+03:00"; got != want {
		t.Fatalf("opens at = %q, want %q", got, want)
	}
	if got, want := items[0].RegistrationClosesAt, "2026-10-19T19:59:59+03:00"; got != want {
		t.Fatalf("closes at = %q, want %q", got, want)
	}
}
