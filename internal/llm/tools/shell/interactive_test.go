package shell

import "testing"

func TestClassifyInteractive(t *testing.T) {
	tests := []struct {
		name    string
		command string
		extra   []string
		want    bool
	}{
		{name: "plain listing", command: "ls -la", want: false},
		{name: "sudo", command: "sudo -v", want: true},
		{name: "sudo with env prefix", command: "FOO=1 BAR=2 sudo apt-get update", want: true},
		{name: "absolute path to sudo", command: "/usr/bin/sudo -v", want: true},
		{name: "ssh", command: "ssh host.example.com", want: true},
		{name: "editor", command: "vim notes.md", want: true},

		// The classifier is a first-token check by design; these are the
		// false-positive traps that a naive substring match would fall into.
		{name: "sudo as an argument", command: "echo sudo", want: false},
		{name: "sudo inside a commit message", command: `git commit -m "run sudo first"`, want: false},
		{name: "sudo later in a pipeline is not detected", command: "cat x | sudo tee y", want: false},
		{name: "program merely starting with a listed name", command: "sudoku --solve", want: false},

		// tty-flagged programs
		{name: "docker ps", command: "docker ps", want: false},
		{name: "docker exec -it", command: "docker exec -it web sh", want: true},
		{name: "docker exec -ti", command: "docker exec -ti web sh", want: true},
		{name: "docker run separate flags", command: "docker run -i -t alpine sh", want: true},
		{name: "docker with long flag", command: "docker exec --interactive web sh", want: true},
		{name: "docker build is not interactive", command: "docker build --tag x .", want: false},
		{name: "kubectl get", command: "kubectl get pods", want: false},
		{name: "kubectl exec -it", command: "kubectl exec -it pod -- sh", want: true},

		// config extension
		{name: "user configured program", command: "my-tool --login", extra: []string{"my-tool"}, want: true},
		{name: "user config is case-insensitive", command: "My-Tool --login", extra: []string{"my-tool"}, want: true},
		{name: "user config with blank entries", command: "ls", extra: []string{"", "  "}, want: false},

		// degenerate input
		{name: "empty", command: "", want: false},
		{name: "only whitespace", command: "   ", want: false},
		{name: "only assignments", command: "FOO=1 BAR=2", want: false},
		{name: "leading equals is not an assignment", command: "=weird", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyInteractive(tt.command, tt.extra); got != tt.want {
				t.Errorf("ClassifyInteractive(%q, %v) = %v, want %v", tt.command, tt.extra, got, tt.want)
			}
		})
	}
}

func TestNeedsTerminal(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "empty", output: "", want: false},
		{
			name:   "sudo without a tty",
			output: "sudo: no tty present and no askpass program specified",
			want:   true,
		},
		{
			name:   "sudo password requirement",
			output: "sudo: a terminal is required to read the password; either use the -S option to read from standard input or configure an askpass helper",
			want:   true,
		},
		{
			name:   "git prompts disabled",
			output: "fatal: could not read Username for 'https://example.com': terminal prompts disabled",
			want:   true,
		},
		{
			name:   "ssh host key",
			output: "Host key verification failed.",
			want:   true,
		},
		{
			name:   "ioctl on a redirected stream",
			output: "stty: 'standard input': Inappropriate ioctl for device",
			want:   true,
		},
		{
			name:   "case is ignored",
			output: "SUDO: NO TTY PRESENT",
			want:   true,
		},
		{
			name:   "ordinary failure gets no hint",
			output: "ls: cannot access 'nope': No such file or directory",
			want:   false,
		},
		{
			name:   "compile error gets no hint",
			output: "./main.go:12:2: undefined: foo",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsTerminal(tt.output); got != tt.want {
				t.Errorf("NeedsTerminal(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}
