package toolenv

import (
	"reflect"
	"testing"
)

func TestDenied(t *testing.T) {
	tests := []struct {
		name    string
		varName string
		entries []string
		want    bool
	}{
		{"exact match", "MYSQL_DSN", []string{"MYSQL_DSN"}, true},
		{"no match", "PATH", []string{"MYSQL_DSN"}, false},
		{"case sensitive", "mysql_dsn", []string{"MYSQL_DSN"}, false},
		{"prefix glob", "AWS_SECRET_ACCESS_KEY", []string{"AWS_SECRET_*"}, true},
		{"prefix glob misses", "AWS_REGION", []string{"AWS_SECRET_*"}, false},
		{"glob is whole-name", "X_AWS_SECRET_KEY", []string{"AWS_SECRET_*"}, false},
		{"question mark", "KEY1", []string{"KEY?"}, true},
		{"character class", "KEY2", []string{"KEY[0-9]"}, true},
		{"malformed glob matches itself", "BAD[", []string{"BAD["}, true},
		{"malformed glob matches nothing else", "BADX", []string{"BAD["}, false},
		{"empty entries", "PATH", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Denied(tt.varName, tt.entries); got != tt.want {
				t.Errorf("Denied(%q, %q) = %v, want %v", tt.varName, tt.entries, got, tt.want)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"OPENCODE_SERVER_PASSWORD=s3cret",
		"HOME=/home/agent",
		"MYSQL_DSN=user:pw@tcp(db)/x",
		"MYSQL_DSN=duplicate",
		"EQUALS_IN_VALUE=a=b",
		"NOEQUALS",
	}
	in := append([]string(nil), env...)
	got := Filter(env, []string{"OPENCODE_SERVER_PASSWORD", "MYSQL_DSN"})
	want := []string{"PATH=/usr/bin", "HOME=/home/agent", "EQUALS_IN_VALUE=a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(env, in) {
		t.Fatalf("Filter modified its input: %q", env)
	}
}

func TestDenyList(t *testing.T) {
	t.Cleanup(func() { SetConfigured(nil) })

	t.Run("built-ins only", func(t *testing.T) {
		SetConfigured(nil)
		t.Setenv(EnvVar, "")
		if got := DenyList(); !reflect.DeepEqual(got, Builtin) {
			t.Fatalf("DenyList = %q, want %q", got, Builtin)
		}
	})

	t.Run("union of config and env, trimmed and deduplicated", func(t *testing.T) {
		SetConfigured([]string{"MYSQL_DSN", " ", "OPENCODE_SERVER_PASSWORD"})
		t.Setenv(EnvVar, " ORC_JOB_TOKEN, ,MYSQL_DSN,AWS_SECRET_*")
		want := append(append([]string(nil), Builtin...), "MYSQL_DSN", "ORC_JOB_TOKEN", "AWS_SECRET_*")
		if got := DenyList(); !reflect.DeepEqual(got, want) {
			t.Fatalf("DenyList = %q, want %q", got, want)
		}
	})

	t.Run("configuration cannot remove a built-in", func(t *testing.T) {
		SetConfigured([]string{})
		t.Setenv(EnvVar, "")
		for _, b := range Builtin {
			if !Denied(b, DenyList()) {
				t.Errorf("%s is not denied", b)
			}
		}
	})
}

func TestEnviron(t *testing.T) {
	t.Cleanup(func() { SetConfigured(nil) })
	SetConfigured([]string{"TOOLENV_TEST_CONFIGURED"})
	t.Setenv(EnvVar, "TOOLENV_TEST_FROM_ENV")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "s3cret")
	t.Setenv("TOOLENV_TEST_CONFIGURED", "a")
	t.Setenv("TOOLENV_TEST_FROM_ENV", "b")
	t.Setenv("TOOLENV_TEST_KEPT", "c")

	got := map[string]bool{}
	for _, kv := range Environ() {
		got[kv] = true
	}
	for _, gone := range []string{"OPENCODE_SERVER_PASSWORD=s3cret", "TOOLENV_TEST_CONFIGURED=a", "TOOLENV_TEST_FROM_ENV=b"} {
		if got[gone] {
			t.Errorf("Environ still contains %s", gone)
		}
	}
	if !got["TOOLENV_TEST_KEPT=c"] {
		t.Error("Environ dropped an undenied variable")
	}
	// The deny-list variable itself carries names only, never values.
	if !got[EnvVar+"=TOOLENV_TEST_FROM_ENV"] {
		t.Errorf("Environ dropped %s", EnvVar)
	}
}

func TestPresent(t *testing.T) {
	env := []string{"PATH=/usr/bin", "OPENCODE_MYSQL_DSN=x", "MYSQL_DSN=y"}
	if got, want := Present(env, []string{"OPENCODE_MYSQL_DSN", "MYSQL_DSN"}), []string{"OPENCODE_MYSQL_DSN", "MYSQL_DSN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Present = %q, want %q", got, want)
	}
	if got := Present([]string{"PATH=/usr/bin"}, Builtin); got != nil {
		t.Fatalf("Present = %q, want nil", got)
	}
}
