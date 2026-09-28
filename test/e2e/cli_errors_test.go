package e2e

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alebeck/boring/internal/daemon"
	"github.com/alebeck/boring/internal/ipc"
	"github.com/alebeck/boring/internal/log"
	"github.com/alebeck/boring/internal/tunnel"
)

// startFakeDaemon serves the daemon protocol from within the test process and
// returns env with BORING_SOCK pointing to it. Nop is answered with the CLI's
// commit, List with listResp.
func startFakeDaemon(t *testing.T, env []string, listResp daemon.Resp) []string {
	t.Helper()
	log.Init(io.Discard, false, false)

	// Unix socket paths are limited to 104 bytes on macOS, which paths
	// based on t.TempDir() can exceed for long test names
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("could not create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("could not listen: %v", err)
	}
	commit := getEnv(env, "BORING_COMMIT_OVERRIDE")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				var cmd daemon.Cmd
				if err := ipc.Read(&cmd, conn); err != nil {
					return
				}
				resp := daemon.Resp{Success: true, Info: daemon.Info{Commit: commit}}
				if cmd.Kind == daemon.List {
					resp = listResp
				}
				ipc.Write(resp, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return setEnv(env, "BORING_SOCK", sock)
}

func expectFatal(t *testing.T, env []string, msg string, args ...string) {
	t.Helper()
	c, out, err := cliCommand(env, args...)
	if err != nil {
		t.Fatalf("failed to run CLI command: %v", err)
	}
	if c != 1 {
		t.Fatalf("exit code %d, expected 1: %s", c, out)
	}
	if !strings.Contains(out, msg) {
		t.Fatalf("output does not contain %q: %s", msg, out)
	}
}

func TestGroupFlagWithoutName(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	msg := "'-g/--group' requires exactly one group name argument."
	expectFatal(t, env, msg, "open", "-g")
	expectFatal(t, env, msg, "list", "--group")
}

func TestListUnknownArgument(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	expectFatal(t, env, "Unknown arguments for 'list'", "list", "foo")
}

func TestOpenNoDaemon(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	expectFatal(t, env, "Startup: not running and BORING_NO_SPAWN is set", "open", "test")
}

func TestListFailure(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	env = startFakeDaemon(t, env, daemon.Resp{Success: false, Error: "list broken"})
	expectFatal(t, env, "Could not list tunnels: list broken", "list")
	expectFatal(t, env, "Could not get running tunnels: list broken", "close", "test")
}

func TestCloseDefaultGroupNotRunning(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	env = startFakeDaemon(t, env, daemon.Resp{Success: true})
	expectFatal(t, env, "No running tunnels in group 'default'.", "close", "-g", "default")
}

func TestListDefaultGroup(t *testing.T) {
	cfg := defaultConfig
	cfg.boringConfig = "../testdata/config/config_groups.toml"
	env, err := makeEnv(cfg, t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	env = startFakeDaemon(t, env, daemon.Resp{Success: true, Tunnels: map[string]tunnel.Desc{
		"dev-web": {Name: "dev-web", Group: "dev", Status: tunnel.Open},
	}})

	c, out, err := cliCommand(env, "list", "-g", "default")
	if err != nil {
		t.Fatalf("failed to run CLI command: %v", err)
	}
	if c != 0 {
		t.Fatalf("exit code %d: %s", c, out)
	}
	if !strings.Contains(out, "misc") {
		t.Errorf("default group tunnel missing: %s", out)
	}
	if strings.Contains(out, "dev-web") || strings.Contains(out, "prod-web") {
		t.Errorf("tunnels of other groups listed: %s", out)
	}
}

func TestEditConfigCreateFails(t *testing.T) {
	cfg := defaultConfig
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}
	cfg.boringConfig = filepath.Join(blocker, "config.toml")
	env, err := makeEnv(cfg, t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	expectFatal(t, env, "could not create config file", "edit")
}

// Without $EDITOR, 'edit' falls back to vi, which we put on PATH as a stub.
func TestEditDefaultEditor(t *testing.T) {
	env, err := makeDefaultEnv(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\necho \"stub vi: $1\"\n"
	if err := os.WriteFile(filepath.Join(bin, "vi"), []byte(stub), 0755); err != nil {
		t.Fatalf("failed to create editor stub: %v", err)
	}
	env = setEnv(env, "EDITOR", "")
	env = setEnv(env, "PATH", bin)

	c, out, err := cliCommand(env, "edit")
	if err != nil {
		t.Fatalf("failed to run CLI command: %v", err)
	}
	if c != 0 {
		t.Fatalf("exit code %d: %s", c, out)
	}
	if !strings.Contains(out, "stub vi: "+defaultConfig.boringConfig) {
		t.Errorf("vi was not invoked with the config path: %s", out)
	}
}
