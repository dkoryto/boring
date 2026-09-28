package e2e

import (
	"strings"
	"testing"

	"golang.org/x/sync/errgroup"
)

// runConcurrently runs the same CLI command n times in parallel and fails
// the test if any of them exits with a non-zero code.
func runConcurrently(t *testing.T, env []string, n int, args ...string) {
	t.Helper()
	runConcurrentlyAllow(t, env, n, nil, args...)
}

// runConcurrentlyAllow is like runConcurrently, but also accepts a non-zero
// exit code if the output contains one of the allowed messages.
func runConcurrentlyAllow(t *testing.T, env []string, n int, allowed []string, args ...string) {
	t.Helper()
	var g errgroup.Group
	outs := make([]string, n)
	codes := make([]int, n)
	for i := range n {
		g.Go(func() error {
			var err error
			codes[i], outs[i], err = cliCommand(env, args...)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("failed to run CLI command: %v", err)
	}
	for i := range n {
		if codes[i] != 0 && !containsAny(outs[i], allowed) {
			t.Fatalf("%v: exit code %d: %s", args, codes[i], outs[i])
		}
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func listStatus(t *testing.T, env []string) string {
	t.Helper()
	c, out, err := cliCommand(env, "list")
	if err != nil {
		t.Fatalf("failed to run CLI command: %v", err)
	}
	if c != 0 {
		t.Fatalf("list: exit code %d: %s", c, out)
	}
	lines := strings.Split(strings.TrimSpace(stripANSI(out)), "\n")
	return strings.Fields(lines[1])[0]
}

// Closing the same tunnel from several clients at once used to crash the
// daemon with "close of closed channel". The daemon is started with
// BORING_NO_SPAWN, so a crash makes the following commands fail.
//
// Clients that lose the race legitimately find the tunnel no longer
// running, either when listing running tunnels or when sending the close
// command, and exit with an error. That is expected here.
var notRunning = []string{"No running tunnels match", "tunnel not running"}

func TestCloseConcurrent(t *testing.T) {
	env, cancel, err := makeDefaultEnvWithDaemon(t)
	if err != nil {
		t.Fatalf("%v", err.Error())
	}
	defer cancel()

	for range 5 {
		runConcurrently(t, env, 1, "open", "test")
		runConcurrentlyAllow(t, env, 4, notRunning, "close", "test")
		if s := listStatus(t, env); s != "closed" {
			t.Fatalf("expected tunnel to be closed, got %q", s)
		}
	}
}

// Opening the same tunnel from several clients at once must open it
// exactly once. The others should report it as already running instead
// of failing to bind the local port.
func TestOpenConcurrent(t *testing.T) {
	env, cancel, err := makeDefaultEnvWithDaemon(t)
	if err != nil {
		t.Fatalf("%v", err.Error())
	}
	defer cancel()

	for range 5 {
		runConcurrently(t, env, 4, "open", "test")
		if s := listStatus(t, env); s == "closed" {
			t.Fatal("expected tunnel to be open")
		}
		testTunnel(t, "localhost:49711", "localhost:49712")
		runConcurrently(t, env, 1, "close", "test")
	}
}

// A tunnel that is reopened right after being closed must stay tracked by
// the daemon, i.e. it shows up in the list and can be closed again.
func TestCloseReopen(t *testing.T) {
	env, cancel, err := makeDefaultEnvWithDaemon(t)
	if err != nil {
		t.Fatalf("%v", err.Error())
	}
	defer cancel()

	runConcurrently(t, env, 1, "open", "test")
	for range 10 {
		runConcurrently(t, env, 1, "close", "test")
		runConcurrently(t, env, 1, "open", "test")
		if s := listStatus(t, env); s == "closed" {
			t.Fatal("reopened tunnel not tracked by the daemon")
		}
	}
	runConcurrently(t, env, 1, "close", "test")
}
