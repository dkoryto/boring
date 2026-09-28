package daemon

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const childEnv = "BORING_DAEMON_TEST_CHILD"

// expectExit runs the calling test again in a child process, where f is
// called, and checks that the child exits with status 1.
func expectExit(t *testing.T, f func()) {
	t.Helper()
	if os.Getenv(childEnv) == t.Name() {
		f()
		t.Fatal("expected process to exit")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		childEnv+"="+t.Name(),
		// Keep the race detector from pausing a second before exiting
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"),
	)
	// Let the child's coverage count towards the parent's. The child exits
	// through os.Exit, which only writes coverage data if GOCOVERDIR is set.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+f.Value.String())
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit status 1, got %v\n%s", err, out)
	}
}

func TestInitLoggingFails(t *testing.T) {
	expectExit(t, func() {
		initLogging(filepath.Join(t.TempDir(), "missing", logFileName))
	})
}

func TestRunListenFails(t *testing.T) {
	expectExit(t, func() {
		dir := t.TempDir()
		LogFile = filepath.Join(dir, logFileName)
		Socket = filepath.Join(dir, "missing", sockName)
		Run()
	})
}
