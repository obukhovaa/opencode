package heartbeat

import (
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestNextBeat(t *testing.T) {
	// 2026-10-01 is a Thursday.
	weekdays := true
	window := &Window{Start: 5 * 60, End: 21 * 60}
	night := &Window{Start: 22 * 60, End: 6 * 60}
	tests := []struct {
		name  string
		after string
		s     Settings
		want  string
	}{
		{"hourly default rounds up", "2026-10-01 10:12", Settings{}, "2026-10-01 11:00"},
		{"exactly on a slot", "2026-10-01 11:00", Settings{}, "2026-10-01 11:00"},
		{"every 30m", "2026-10-01 10:12", Settings{Every: 30 * time.Minute}, "2026-10-01 10:30"},
		{"window before start", "2026-10-01 02:00", Settings{Window: window}, "2026-10-01 05:00"},
		{"window end is exclusive", "2026-10-01 20:30", Settings{Window: window}, "2026-10-02 05:00"},
		{"wrapping window late", "2026-10-01 21:30", Settings{Window: night}, "2026-10-01 22:00"},
		{"wrapping window early morning", "2026-10-01 03:10", Settings{Window: night}, "2026-10-01 04:00"},
		{"wrapping window daytime", "2026-10-01 07:00", Settings{Window: night}, "2026-10-01 22:00"},
		{"friday evening skips the weekend", "2026-10-02 21:30", Settings{Window: window, WeekdaysOnly: weekdays}, "2026-10-05 05:00"},
		{"saturday", "2026-10-03 12:00", Settings{WeekdaysOnly: weekdays}, "2026-10-05 00:00"},
		{"interval that does not divide a day restarts at midnight", "2026-10-01 22:00", Settings{Every: 7 * time.Hour}, "2026-10-02 00:00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NextBeat(utc(tc.after), tc.s)
			if want := utc(tc.want); !got.Equal(want) {
				t.Fatalf("NextBeat(%s) = %s, want %s", tc.after, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	c, err := ParseCommand("on every 30m hours 05-21 days weekdays model claude-x file notes/agenda.md")
	if err != nil {
		t.Fatal(err)
	}
	got := c.Apply(Settings{State: StateUnset})
	if got.State != StateOn || got.Every != 30*time.Minute || got.Window == nil ||
		*got.Window != (Window{Start: 300, End: 1260}) || !got.WeekdaysOnly ||
		got.Model != "claude-x" || got.AgendaFile != "notes/agenda.md" {
		t.Fatalf("unexpected settings %+v window %+v", got, got.Window)
	}

	c, err = ParseCommand("hours all days all model default file default")
	if err != nil {
		t.Fatal(err)
	}
	cleared := c.Apply(got)
	if cleared.Window != nil || cleared.WeekdaysOnly || cleared.Model != "" || cleared.AgendaFile != "" || cleared.State != StateOn {
		t.Fatalf("defaults not restored: %+v", cleared)
	}

	c, err = ParseCommand("hours 22:30-06")
	if err != nil {
		t.Fatal(err)
	}
	if *c.Window != (Window{Start: 22*60 + 30, End: 360}) {
		t.Fatalf("window %+v", c.Window)
	}
	if c, err = ParseCommand("hours 05-24"); err != nil || c.Window.End != 0 {
		t.Fatalf("05-24: %+v %v", c.Window, err)
	}

	for _, empty := range []string{"", "status", "  "} {
		c, err := ParseCommand(empty)
		if err != nil || c.ChangesSettings() || c.Now {
			t.Fatalf("%q should be a pure status request: %+v %v", empty, c, err)
		}
	}
	if c, _ := ParseCommand("now"); !c.Now || c.ChangesSettings() {
		t.Fatal("now must not change settings")
	}
}

func TestParseCommandErrors(t *testing.T) {
	for _, args := range []string{
		"every 2m", "every 25h", "every soon", "every",
		"hours 5", "hours 05-05", "hours 00-24", "hours 25-03", "hours 05:7-09",
		"days monday", "file /etc/passwd", "file ../x", "file ~/x",
		"model", "sometimes",
	} {
		if _, err := ParseCommand(args); err == nil {
			t.Errorf("ParseCommand(%q) succeeded, want an error", args)
		}
	}
	_, err := ParseCommand("every 2m")
	if !strings.Contains(err.Error(), "between 10m and 24h") {
		t.Fatalf("error should name the bounds: %v", err)
	}
}

func TestIsSilentAck(t *testing.T) {
	long := "HEARTBEAT_OK\n" + strings.Repeat("x", 400)
	tests := map[string]bool{
		"HEARTBEAT_OK":                        true,
		"  heartbeat_ok \n":                   true,
		"**HEARTBEAT_OK**":                    true,
		"`HEARTBEAT_OK`.":                     true,
		"Nothing new.\nHEARTBEAT_OK":          true,
		"HEARTBEAT_OK\nchecked 3 tickets":     true,
		long:                                  false,
		"":                                    false,
		"CI failed on main":                   false,
		"Not HEARTBEAT_OK: CI failed on main": false,
	}
	for in, want := range tests {
		if got := IsSilentAck(in); got != want {
			t.Errorf("IsSilentAck(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAgendaIsEmpty(t *testing.T) {
	empty := []string{"", "   \n\n", "# Heartbeat\n\n## Every beat\n- \n- [ ]\n", "<!-- add items -->\n```\n```\n---\n"}
	for _, s := range empty {
		if !AgendaIsEmpty(s) {
			t.Errorf("AgendaIsEmpty(%q) = false, want true", s)
		}
	}
	if AgendaIsEmpty("# Heartbeat\n- Check open merge requests\n") {
		t.Error("a list item with text is an instruction")
	}
}

func TestDescribe(t *testing.T) {
	r := Record{
		Settings:   Settings{State: StateOn, Every: 30 * time.Minute, Window: &Window{Start: 300, End: 1260}, WeekdaysOnly: true},
		NextBeatAt: utc("2026-10-01 11:30"),
		LastBeatAt: utc("2026-10-01 11:00"),
		LastStatus: OutcomeSilent,
	}
	got := Describe(r, "model-a")
	for _, want := range []string{"is on", "Every: 30m", "Hours: 05:00-21:00 UTC", "Days: weekdays", "Model: model-a (the agent's own)", "Last beat: 2026-10-01 11:00Z, silent", "Next beat: 2026-10-01 11:30Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe missing %q:\n%s", want, got)
		}
	}
	if s := shortDuration(70 * time.Minute); s != "1h10m" {
		t.Errorf("shortDuration(70m) = %q", s)
	}
	if s := shortDuration(2 * time.Hour); s != "2h" {
		t.Errorf("shortDuration(2h) = %q", s)
	}
}
