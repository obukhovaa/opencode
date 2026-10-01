// Package heartbeat holds the pure parts of the daemon heartbeat: the
// settings a chat binding carries, the schedule that turns them into the
// next beat time, the /heartbeat command grammar, the heartbeat prompt and
// the silent-acknowledgement contract. It has no storage and no I/O; the
// bridge (internal/bridge/service) owns scheduling, persistence and
// delivery. See openspec capability bridge-heartbeat.
package heartbeat

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// State is a binding's heartbeat switch. Unset means the human has never
// chosen; only Unset bindings receive the setup reminder.
type State string

const (
	StateUnset State = "unset"
	StateOn    State = "on"
	StateOff   State = "off"
)

const (
	// DefaultEvery is the interval when none has been set.
	DefaultEvery = time.Hour
	// MinEvery and MaxEvery bound `/heartbeat every`.
	MinEvery = 10 * time.Minute
	MaxEvery = 24 * time.Hour
	// DefaultAgendaFile is the checklist each beat reads, relative to the
	// working directory.
	DefaultAgendaFile = "HEARTBEAT.md"
	// ReminderInterval is the minimum gap between two setup reminders for
	// one binding.
	ReminderInterval = 7 * 24 * time.Hour
	// SilentToken is the reply that means "nothing to report".
	SilentToken = "HEARTBEAT_OK"
	// silentAckMaxRunes caps a reply that may carry SilentToken on its
	// first or last line and still be dropped.
	silentAckMaxRunes = 300
)

// Window is an active-hours window in minutes after 00:00 UTC. Start is
// inclusive, End exclusive; End < Start wraps midnight.
type Window struct {
	Start int
	End   int
}

func (w Window) contains(minute int) bool {
	if w.Start < w.End {
		return minute >= w.Start && minute < w.End
	}
	return minute >= w.Start || minute < w.End
}

func (w Window) String() string {
	return fmt.Sprintf("%s-%s", clock(w.Start), clock(w.End))
}

func clock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// Settings is one binding's heartbeat configuration. Zero values mean the
// default: hourly, all hours, every day, the agent's own model, and
// DefaultAgendaFile.
type Settings struct {
	State        State
	Every        time.Duration
	Window       *Window
	WeekdaysOnly bool
	Model        string
	AgendaFile   string
}

// EffectiveEvery returns the interval in force.
func (s Settings) EffectiveEvery() time.Duration {
	if s.Every <= 0 {
		return DefaultEvery
	}
	return s.Every
}

// EffectiveAgendaFile returns the agenda path in force.
func (s Settings) EffectiveAgendaFile() string {
	if s.AgendaFile == "" {
		return DefaultAgendaFile
	}
	return s.AgendaFile
}

// NextBeat returns the first slot at or after `after` that the settings
// allow. Slots lie on a grid of the interval anchored at 00:00 UTC of each
// day, restricted to the active window and days. Computing from "now"
// rather than from a missed due time is what coalesces missed beats.
func NextBeat(after time.Time, s Settings) time.Time {
	after = after.UTC()
	every := s.EffectiveEvery()
	day := time.Date(after.Year(), after.Month(), after.Day(), 0, 0, 0, 0, time.UTC)
	// Eight days covers a weekend plus any window; the loop always finds a
	// slot because a window is never empty.
	for d := 0; d < 8; d++ {
		midnight := day.AddDate(0, 0, d)
		if s.WeekdaysOnly && isWeekend(midnight.Weekday()) {
			continue
		}
		for slot := midnight; slot.Before(midnight.Add(24 * time.Hour)); slot = slot.Add(every) {
			if slot.Before(after) {
				continue
			}
			if s.Window != nil && !s.Window.contains(int(slot.Sub(midnight)/time.Minute)) {
				continue
			}
			return slot
		}
	}
	return after.Add(every)
}

func isWeekend(d time.Weekday) bool {
	return d == time.Saturday || d == time.Sunday
}

// Command is a parsed `/heartbeat` invocation. Pointer fields are nil when
// the command does not touch that setting.
type Command struct {
	On, Off, Now bool
	Every        *time.Duration
	// Window is set when `hours` appears; ClearWindow means `hours all`.
	Window      *Window
	ClearWindow bool
	Weekdays    *bool
	Model       *string // "" means back to the default
	AgendaFile  *string // "" means back to the default
}

// ChangesSettings reports whether applying the command alters stored
// settings (as opposed to a pure status or `now` request).
func (c Command) ChangesSettings() bool {
	return c.On || c.Off || c.Every != nil || c.Window != nil || c.ClearWindow ||
		c.Weekdays != nil || c.Model != nil || c.AgendaFile != nil
}

// Apply returns s with the command's changes.
func (c Command) Apply(s Settings) Settings {
	switch {
	case c.On:
		s.State = StateOn
	case c.Off:
		s.State = StateOff
	}
	if c.Every != nil {
		s.Every = *c.Every
	}
	if c.ClearWindow {
		s.Window = nil
	}
	if c.Window != nil {
		w := *c.Window
		s.Window = &w
	}
	if c.Weekdays != nil {
		s.WeekdaysOnly = *c.Weekdays
	}
	if c.Model != nil {
		s.Model = *c.Model
	}
	if c.AgendaFile != nil {
		s.AgendaFile = *c.AgendaFile
	}
	return s
}

// Usage is the one-line grammar shown with a parse error.
const Usage = "usage: /heartbeat [status|on|off|now] [every 30m|1h|…] [hours 05-21|all] [days weekdays|all] [model <id>|default] [file <path>|default]"

var errMissingValue = errors.New("missing value")

// ParseCommand parses the arguments after `/heartbeat`. An empty string is
// a status request. Model IDs are not validated here (the caller knows the
// supported set).
func ParseCommand(args string) (Command, error) {
	var c Command
	words := strings.Fields(args)
	for i := 0; i < len(words); i++ {
		word := strings.ToLower(words[i])
		next := func() (string, error) {
			if i+1 >= len(words) {
				return "", fmt.Errorf("%q: %w", word, errMissingValue)
			}
			i++
			return words[i], nil
		}
		switch word {
		case "status":
		case "on":
			c.On, c.Off = true, false
		case "off":
			c.Off, c.On = true, false
		case "now":
			c.Now = true
		case "every":
			v, err := next()
			if err != nil {
				return Command{}, err
			}
			d, err := parseEvery(v)
			if err != nil {
				return Command{}, err
			}
			c.Every = &d
		case "hours":
			v, err := next()
			if err != nil {
				return Command{}, err
			}
			if strings.EqualFold(v, "all") {
				c.Window, c.ClearWindow = nil, true
				continue
			}
			w, err := parseWindow(v)
			if err != nil {
				return Command{}, err
			}
			c.Window, c.ClearWindow = &w, false
		case "days":
			v, err := next()
			if err != nil {
				return Command{}, err
			}
			var weekdays bool
			switch strings.ToLower(v) {
			case "weekdays":
				weekdays = true
			case "all":
			default:
				return Command{}, fmt.Errorf("days %q: want weekdays or all", v)
			}
			c.Weekdays = &weekdays
		case "model":
			v, err := next()
			if err != nil {
				return Command{}, err
			}
			if strings.EqualFold(v, "default") {
				v = ""
			}
			c.Model = &v
		case "file":
			v, err := next()
			if err != nil {
				return Command{}, err
			}
			if strings.EqualFold(v, "default") {
				v = ""
			} else if err := validateAgendaFile(v); err != nil {
				return Command{}, err
			}
			c.AgendaFile = &v
		default:
			return Command{}, fmt.Errorf("unknown argument %q", words[i])
		}
	}
	return c, nil
}

func parseEvery(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.ToLower(v))
	if err != nil {
		return 0, fmt.Errorf("every %q: not a duration (try 30m, 1h, 2h30m)", v)
	}
	if d < MinEvery || d > MaxEvery {
		return 0, fmt.Errorf("every %q: the interval must be between %s and %s", v, shortDuration(MinEvery), shortDuration(MaxEvery))
	}
	return d, nil
}

func parseWindow(v string) (Window, error) {
	start, end, ok := strings.Cut(v, "-")
	if !ok {
		return Window{}, fmt.Errorf("hours %q: want HH-HH or HH:MM-HH:MM (UTC), or all", v)
	}
	s, err := parseClock(start)
	if err != nil {
		return Window{}, fmt.Errorf("hours %q: %w", v, err)
	}
	e, err := parseClock(end)
	if err != nil {
		return Window{}, fmt.Errorf("hours %q: %w", v, err)
	}
	if s == e || (s == 0 && e == 24*60) {
		return Window{}, fmt.Errorf("hours %q: start and end must differ (use hours all for the whole day)", v)
	}
	if s == 24*60 {
		return Window{}, fmt.Errorf("hours %q: start must be before 24", v)
	}
	return Window{Start: s, End: e % (24 * 60)}, nil
}

func parseClock(v string) (int, error) {
	h, m, hasMinutes := strings.Cut(v, ":")
	hour, err := strconv.Atoi(h)
	if err != nil || hour < 0 || hour > 24 {
		return 0, fmt.Errorf("%q is not an hour 0-24", v)
	}
	minute := 0
	if hasMinutes {
		minute, err = strconv.Atoi(m)
		if err != nil || minute < 0 || minute > 59 || len(m) != 2 {
			return 0, fmt.Errorf("%q is not a time HH:MM", v)
		}
	}
	if hour == 24 && minute != 0 {
		return 0, fmt.Errorf("%q is past 24:00", v)
	}
	return hour*60 + minute, nil
}

func validateAgendaFile(p string) error {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return fmt.Errorf("file %q: give a path relative to the working directory", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return fmt.Errorf("file %q: the path may not leave the working directory", p)
		}
	}
	return nil
}

// IsSilentAck reports whether a beat's final reply means "nothing to
// report": the token alone, or a short reply whose first or last line is
// the token. Markdown emphasis and code markers around the token are
// tolerated.
func IsSilentAck(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false
	}
	if isToken(reply) {
		return true
	}
	if utf8.RuneCountInString(reply) > silentAckMaxRunes {
		return false
	}
	lines := strings.Split(reply, "\n")
	return isToken(lines[0]) || isToken(lines[len(lines)-1])
}

func isToken(s string) bool {
	s = strings.Trim(strings.TrimSpace(s), "*_`.")
	return strings.EqualFold(s, SilentToken)
}

// AgendaIsEmpty reports whether an agenda file holds no instructions:
// only whitespace, markdown headings, empty list items, horizontal rules,
// code fences and one-line HTML comments.
func AgendaIsEmpty(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
		case strings.HasPrefix(t, "```"):
		case strings.HasPrefix(t, "<!--") && strings.HasSuffix(t, "-->"):
		case isBareListMarker(t):
		default:
			return false
		}
	}
	return true
}

func isBareListMarker(t string) bool {
	t = strings.TrimSpace(strings.TrimLeft(t, "-*+"))
	return t == "" || t == "[ ]" || t == "[x]" || t == "---"
}

// Prompt is the text of a heartbeat turn.
func Prompt(now time.Time, agendaFile string) string {
	return fmt.Sprintf(`[Heartbeat %s] This is a scheduled heartbeat turn, not a message from your human.

Read %s in the working directory and follow it. It is your standing agenda for heartbeats; keep it up to date when your human gives you standing instructions.
Report only what is new since your last heartbeat. Do not repeat old items from earlier in the conversation unless the agenda says they are still open.
If nothing needs your human's attention, reply with exactly %s and nothing else.`,
		now.UTC().Format("2006-01-02 15:04Z"), agendaFile, SilentToken)
}

// Header is the line prefixed to a posted beat report.
func Header(now time.Time) string {
	return "💓 Heartbeat " + now.UTC().Format("15:04Z")
}

// Outcome of one beat, as recorded on the binding's row.
const (
	OutcomeOK      = "ok"
	OutcomeSilent  = "silent"
	OutcomeSkipped = "skipped"
	OutcomeError   = "error"
)

// Record is the bookkeeping shown by /heartbeat status. Zero times mean
// "never" / "not scheduled".
type Record struct {
	Settings
	NextBeatAt time.Time
	LastBeatAt time.Time
	LastStatus string
	LastError  string
}

// Describe renders /heartbeat status.
func Describe(r Record, defaultModel string) string {
	var b strings.Builder
	switch r.State {
	case StateOn:
		b.WriteString("💓 Heartbeat is on.")
	case StateOff:
		b.WriteString("💓 Heartbeat is off.")
	default:
		b.WriteString("💓 Heartbeat is not set up. Turn it on with /heartbeat on.")
	}
	fmt.Fprintf(&b, "\n• Every: %s", shortDuration(r.EffectiveEvery()))
	if r.Window != nil {
		fmt.Fprintf(&b, "\n• Hours: %s UTC", r.Window)
	} else {
		b.WriteString("\n• Hours: all day")
	}
	if r.WeekdaysOnly {
		b.WriteString("\n• Days: weekdays")
	} else {
		b.WriteString("\n• Days: every day")
	}
	if r.Model != "" {
		fmt.Fprintf(&b, "\n• Model: %s", r.Model)
	} else if defaultModel != "" {
		fmt.Fprintf(&b, "\n• Model: %s (the agent's own)", defaultModel)
	}
	fmt.Fprintf(&b, "\n• Agenda: %s", r.EffectiveAgendaFile())
	if !r.LastBeatAt.IsZero() {
		fmt.Fprintf(&b, "\n• Last beat: %s, %s", r.LastBeatAt.UTC().Format("2006-01-02 15:04Z"), r.LastStatus)
		if r.LastError != "" {
			fmt.Fprintf(&b, " (%s)", r.LastError)
		}
	}
	if r.State == StateOn && !r.NextBeatAt.IsZero() {
		fmt.Fprintf(&b, "\n• Next beat: %s", r.NextBeatAt.UTC().Format("2006-01-02 15:04Z"))
	}
	return b.String()
}

// Reminder is the setup message posted to an unset binding.
func Reminder() string {
	return "💓 I can check in on my own with a heartbeat: on a schedule I read my agenda (" + DefaultAgendaFile + ") and tell you only what's new.\n" +
		"• /heartbeat on starts hourly beats.\n" +
		"• /heartbeat on every 30m hours 05-21 days weekdays sets a schedule (UTC).\n" +
		"• /heartbeat off turns it off and stops this reminder.\n" +
		"I'll ask again in a week if you don't choose."
}

func shortDuration(d time.Duration) string {
	s := strings.TrimSuffix(d.String(), "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
