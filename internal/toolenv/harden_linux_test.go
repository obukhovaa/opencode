//go:build linux

package toolenv

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The hardening is process-wide and irreversible, so it is exercised in a
// re-executed copy of the test binary rather than in the test process.
const helperEnv = "TOOLENV_HARDEN_HELPER"

func TestHardenHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper process only")
	}
	names, hardened, err := HardenProcess()
	if err != nil {
		fmt.Printf("ERR %v\n", err)
		os.Exit(0)
	}
	fmt.Printf("HARDENED %v NAMES %s\n", hardened, strings.Join(names, ","))
	// A same-user child tries to read the parent's initial environment.
	out, err := exec.Command("cat", fmt.Sprintf("/proc/%d/environ", os.Getpid())).CombinedOutput()
	fmt.Printf("READ_OK %v\n", err == nil)
	if strings.Contains(string(out), "harden-s3cret") {
		fmt.Println("LEAKED")
	}
	os.Exit(0)
}

func runHardenHelper(t *testing.T, extraEnv ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenHelperProcess$", "-test.v")
	env := Filter(os.Environ(), append([]string{EnvVar}, Builtin...))
	cmd.Env = append(append(env, helperEnv+"=1"), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	return string(out)
}

func TestHardenProcess_HidesEnvironWhenSecretsPresent(t *testing.T) {
	out := runHardenHelper(t, "OPENCODE_SERVER_PASSWORD=harden-s3cret")
	if !strings.Contains(out, "HARDENED true NAMES OPENCODE_SERVER_PASSWORD") {
		t.Fatalf("process not hardened:\n%s", out)
	}
	if !strings.Contains(out, "READ_OK false") || strings.Contains(out, "LEAKED") {
		t.Fatalf("a same-user child read the parent's environ:\n%s", out)
	}
}

func TestHardenProcess_LeavesProcessAloneWithoutSecrets(t *testing.T) {
	out := runHardenHelper(t)
	if !strings.Contains(out, "HARDENED false NAMES \n") {
		t.Fatalf("process hardened with nothing to hide:\n%s", out)
	}
	if !strings.Contains(out, "READ_OK true") {
		t.Fatalf("environ unreadable although nothing was hidden:\n%s", out)
	}
}
