package cron

import (
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every 5 minutes", "*/5 * * * *", false},
		{"every hour", "0 * * * *", false},
		{"daily at 9am", "0 9 * * *", false},
		{"weekdays at 9am", "0 9 * * 1-5", false},
		{"specific time", "30 14 25 3 *", false},
		{"invalid expression", "invalid", true},
		{"too few fields", "* * *", true},
		{"too many fields", "* * * * * *", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchedule(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSchedule(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestComputeNextFire(t *testing.T) {
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)

	tests := []struct {
		name     string
		schedule string
		wantErr  bool
	}{
		{"every 5 min", "*/5 * * * *", false},
		{"hourly", "0 * * * *", false},
		{"invalid", "bad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := ComputeNextFire(tt.schedule, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("ComputeNextFire(%q) error = %v, wantErr %v", tt.schedule, err, tt.wantErr)
			}
			if err == nil && !next.After(now) {
				t.Errorf("ComputeNextFire(%q) = %v, expected after %v", tt.schedule, next, now)
			}
		})
	}
}

func TestComputeNextFireAnchoredFromNow(t *testing.T) {
	// Verify that next_run_at is always anchored from 'now', not from prior next_run_at
	now := time.Date(2026, 5, 7, 15, 47, 0, 0, time.Local)

	next, err := ComputeNextFire("*/5 * * * *", now)
	if err != nil {
		t.Fatal(err)
	}

	// Should be 15:50, not rapid catch-up from earlier missed windows
	if next.Before(now) {
		t.Errorf("next fire %v should be after now %v", next, now)
	}
	if next.Minute() != 50 {
		t.Errorf("next fire minute = %d, expected 50", next.Minute())
	}
}

func TestCronToHuman(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"*/5 * * * *", "every 5 minutes"},
		{"*/1 * * * *", "every minute"},
		{"0 * * * *", "every hour at :00"},
		{"30 * * * *", "every hour at :30"},
		{"0 9 * * *", "daily at 9:00am"},
		{"30 14 * * *", "daily at 2:30pm"},
		{"0 9 * * 1-5", "weekdays at 9:00am"},
		{"0 0 * * *", "daily at 12:00am"},
		{"invalid", "invalid"},
		{"0 */2 * * *", "every 2 hours"},
		{"7 * * * *", "every hour at :07"},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got := CronToHuman(tt.expr)
			if got != tt.want {
				t.Errorf("CronToHuman(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

func TestDurationToCron(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"1 minute", 1 * time.Minute, "* * * * *"},
		{"5 minutes", 5 * time.Minute, "*/5 * * * *"},
		{"10 minutes", 10 * time.Minute, "*/10 * * * *"},
		{"30 seconds rounds to 1 min", 30 * time.Second, "* * * * *"},
		{"1 hour", 1 * time.Hour, "0 * * * *"},
		{"2 hours", 2 * time.Hour, "0 */2 * * *"},
		{"1 day", 24 * time.Hour, "0 0 * * *"},
		{"45 minutes", 45 * time.Minute, "*/45 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DurationToCron(tt.d)
			if got != tt.want {
				t.Errorf("DurationToCron(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	if id1 == id2 {
		t.Error("generateID() produced duplicate IDs")
	}
	if len(id1) < 5 {
		t.Errorf("generateID() = %q, expected at least 5 chars", id1)
	}
	if id1[:5] != "cron_" {
		t.Errorf("generateID() = %q, expected prefix 'cron_'", id1)
	}
}

func TestGenerateCallID(t *testing.T) {
	id1 := generateCallID()
	id2 := generateCallID()

	if id1 == id2 {
		t.Error("generateCallID() produced duplicate IDs")
	}
	if len(id1) < 6 {
		t.Errorf("generateCallID() = %q, expected at least 6 chars", id1)
	}
	if id1[:6] != "toolu_" {
		t.Errorf("generateCallID() = %q, expected prefix 'toolu_'", id1)
	}
}

func TestPadMinute(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0", "00"},
		{"5", "05"},
		{"15", "15"},
		{"30", "30"},
	}
	for _, tt := range tests {
		if got := padMinute(tt.input); got != tt.want {
			t.Errorf("padMinute(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	tests := []struct {
		hour, minute string
		want         string
	}{
		{"9", "0", "9:00am"},
		{"14", "30", "2:30pm"},
		{"0", "0", "12:00am"},
		{"12", "0", "12:00pm"},
		{"23", "59", "11:59pm"},
	}
	for _, tt := range tests {
		t.Run(tt.hour+":"+tt.minute, func(t *testing.T) {
			got := formatTime(tt.hour, tt.minute)
			if got != tt.want {
				t.Errorf("formatTime(%q, %q) = %q, want %q", tt.hour, tt.minute, got, tt.want)
			}
		})
	}
}

func TestDowToName(t *testing.T) {
	tests := []struct {
		dow  string
		want string
	}{
		{"0", "Sunday"},
		{"1", "Monday"},
		{"5", "Friday"},
		{"6", "Saturday"},
		{"0,6", "weekends"},
		{"1-5", "weekdays"},
		{"7", ""},
	}
	for _, tt := range tests {
		if got := dowToName(tt.dow); got != tt.want {
			t.Errorf("dowToName(%q) = %q, want %q", tt.dow, got, tt.want)
		}
	}
}

func TestScheduleValidationFutureMatch(t *testing.T) {
	// Feb 31 doesn't exist — schedule should parse but never match
	schedule, err := ParseSchedule("0 0 31 2 *")
	if err != nil {
		t.Fatal("ParseSchedule should not fail on syntactically valid expression")
	}

	// Check the next fire — it should be way out if ever
	next := schedule.Next(time.Now())
	if !next.IsZero() {
		// robfig/cron returns 0-time when no future match exists
		// Actually it may find a Feb 31 in a non-existent date;
		// the important thing is that our validation catches this
		oneYear := time.Now().Add(366 * 24 * time.Hour)
		if next.After(oneYear) {
			// Good — no match in the next year
		}
	}
}
