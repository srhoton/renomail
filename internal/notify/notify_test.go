package notify

import (
	"errors"
	"testing"
)

func TestTmux_invokesDisplayMessage(t *testing.T) {
	var gotName string
	var gotArgs []string
	orig := run
	t.Cleanup(func() { run = orig })
	run = func(name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	}

	if err := Tmux("3 new items"); err != nil {
		t.Fatalf("Tmux() error = %v", err)
	}
	if gotName != "tmux" {
		t.Errorf("command = %q, want tmux", gotName)
	}
	want := []string{"display-message", "--", "3 new items"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
}

func TestTmux_propagatesRunError(t *testing.T) {
	orig := run
	t.Cleanup(func() { run = orig })
	boom := errors.New("no tmux server")
	run = func(string, ...string) error { return boom }

	if err := Tmux("x"); !errors.Is(err, boom) {
		t.Errorf("Tmux() error = %v, want %v", err, boom)
	}
}

// TestRun_defaultRunnerReportsExecErrors exercises the real exec-backed runner
// (not the test stub) so the default wrapper is covered. A binary that cannot
// exist keeps the assertion deterministic and independent of $PATH contents.
func TestRun_defaultRunnerReportsExecErrors(t *testing.T) {
	if err := run("renomail-no-such-binary-xyzzy", "arg"); err == nil {
		t.Error("run(nonexistent) = nil, want an exec error")
	}
}
