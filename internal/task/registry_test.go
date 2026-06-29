package task

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestNewTaskID_FormatAndUniqueness(t *testing.T) {
	patterns := map[Kind]*regexp.Regexp{
		KindBash:    regexp.MustCompile(`^shell_[A-Z2-7]{26}$`),
		KindTask:    regexp.MustCompile(`^agent_[A-Z2-7]{26}$`),
		KindMonitor: regexp.MustCompile(`^monitor_[A-Z2-7]{26}$`),
		KindCron:    regexp.MustCompile(`^cron_[A-Z2-7]{26}$`),
	}
	for kind, re := range patterns {
		id := NewTaskID(kind)
		if !re.MatchString(id) {
			t.Errorf("kind=%s: id %q does not match %s", kind, id, re)
		}
	}
	seen := make(map[string]struct{}, 10_000)
	for i := range 10_000 {
		id := NewTaskID(KindBash)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iteration %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	id := NewTaskID(KindBash)
	tk := &Task{ID: id, SessionID: "s1", Kind: KindBash, OutputPath: filepath.Join(dir, "tasks", id+".out")}
	if err := r.Register(tk); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, ok := r.Get(id)
	if !ok || got != tk {
		t.Fatalf("get: want %v, got %v ok=%v", tk, got, ok)
	}
	// Duplicate Register must fail.
	if err := r.Register(tk); err == nil {
		t.Fatal("duplicate register did not error")
	}
	if got.State() != StateRunning {
		t.Errorf("state after register: want running, got %v", got.State())
	}
}

func TestRegistry_ListBySession(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	for i, sess := range []string{"a", "a", "b", "a"} {
		_ = i
		_ = r.Register(&Task{ID: NewTaskID(KindBash), SessionID: sess, Kind: KindBash})
	}
	a := r.ListBySession("a")
	b := r.ListBySession("b")
	c := r.ListBySession("c")
	if len(a) != 3 || len(b) != 1 || len(c) != 0 {
		t.Fatalf("list: a=%d b=%d c=%d", len(a), len(b), len(c))
	}
}

func TestRegistry_MarkFinishedIdempotent(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	id := NewTaskID(KindBash)
	tk := &Task{ID: id, SessionID: "s1", Kind: KindBash}
	_ = r.Register(tk)

	code := 0
	r.MarkFinished(id, StateCompleted, &code)
	if tk.State() != StateCompleted {
		t.Fatalf("state: want completed, got %v", tk.State())
	}
	if _, ok := tk.ExitCode(); !ok {
		t.Fatal("exit code not recorded")
	}

	// Idempotence: second MarkFinished does not overwrite.
	code2 := 99
	r.MarkFinished(id, StateFailed, &code2)
	if tk.State() != StateCompleted {
		t.Fatalf("second MarkFinished overwrote: %v", tk.State())
	}
	c, _ := tk.ExitCode()
	if c != 0 {
		t.Fatalf("exit code overwritten: %d", c)
	}
}

func TestRegistry_KillUnknown(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	if err := r.Kill("nope"); err == nil {
		t.Fatal("kill of unknown id did not error")
	}
}

func TestRegistry_PrepareOutputFile(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	id := NewTaskID(KindBash)
	path, f, err := r.PrepareOutputFile(id)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer f.Close()
	if path != filepath.Join(dir, "tasks", id+".out") {
		t.Errorf("path: %s", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode: %o", st.Mode().Perm())
	}
	// O_EXCL: second prepare with same ID fails.
	if _, _, err := r.PrepareOutputFile(id); err == nil {
		t.Fatal("second prepare did not error")
	}
}

func TestRegistry_SweepOrphans(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })
	// One live task, one orphan, one unrelated file.
	id := NewTaskID(KindBash)
	if err := r.Register(&Task{ID: id, SessionID: "s", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	liveFile := filepath.Join(tasksDir, id+".out")
	orphanFile := filepath.Join(tasksDir, "shell_ORPHANXYZXYZXYZXYZXYZXYZXY.out")
	unrelated := filepath.Join(tasksDir, "notes.txt")
	for _, p := range []string{liveFile, orphanFile, unrelated} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r.SweepOrphans(dir)

	if _, err := os.Stat(liveFile); err != nil {
		t.Errorf("live file removed: %v", err)
	}
	if _, err := os.Stat(orphanFile); !os.IsNotExist(err) {
		t.Errorf("orphan file not removed (err=%v)", err)
	}
	// Unrelated non-.out files are left alone.
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated file removed: %v", err)
	}
}

func TestSetGlobalRegistry(t *testing.T) {
	ResetGlobalRegistry()
	if GlobalRegistry() != nil {
		t.Fatal("global registry not nil after reset")
	}
	r := NewRegistry(func() string { return t.TempDir() })
	SetGlobalRegistry(r)
	if GlobalRegistry() != r {
		t.Fatal("global registry mismatch")
	}
	// Second SetGlobalRegistry panics.
	defer func() {
		if recover() == nil {
			t.Fatal("second SetGlobalRegistry did not panic")
		}
		ResetGlobalRegistry()
	}()
	SetGlobalRegistry(r)
}
