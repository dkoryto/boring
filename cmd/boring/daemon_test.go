package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/buildinfo"
	"github.com/alebeck/boring/internal/config"
	"github.com/alebeck/boring/internal/daemon"
	"github.com/alebeck/boring/internal/ipc"
	"github.com/alebeck/boring/internal/log"
	"github.com/alebeck/boring/internal/tunnel"
)

func TestMain(m *testing.M) {
	// ensureDaemon may spawn os.Executable() with the daemon flag,
	// which is this test binary. Exit right away in that case.
	if len(os.Args) == 2 && os.Args[1] == daemon.Flag {
		os.Exit(0)
	}
	log.Init(io.Discard, true, false)
	os.Exit(m.Run())
}

// fakeDaemon serves the daemon's IPC protocol on a unix socket, answering
// each command with whatever handle returns. A nil response closes the
// connection without answering.
type fakeDaemon struct {
	ln        net.Listener
	handle    func(daemon.Cmd) *daemon.Resp
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// withSocket points daemon.Socket to a fresh, short path for the duration
// of the test (macOS limits unix socket paths to 104 bytes).
func withSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	orig := daemon.Socket
	daemon.Socket = filepath.Join(dir, "d.sock")
	t.Cleanup(func() {
		daemon.Socket = orig
		os.RemoveAll(dir)
	})
	return daemon.Socket
}

func startFakeDaemon(t *testing.T, handle func(daemon.Cmd) *daemon.Resp) *fakeDaemon {
	t.Helper()
	sock := withSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{ln: ln, handle: handle}
	d.wg.Add(1)
	go d.serve()
	t.Cleanup(d.close)
	return d
}

func (d *fakeDaemon) serve() {
	defer d.wg.Done()
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer conn.Close()
			var cmd daemon.Cmd
			if err := ipc.Read(&cmd, conn); err != nil {
				return
			}
			if resp := d.handle(cmd); resp != nil {
				ipc.Write(resp, conn)
			}
		}()
	}
}

// stop closes the listener, which also removes the socket file.
func (d *fakeDaemon) stop() {
	d.closeOnce.Do(func() { d.ln.Close() })
}

func (d *fakeDaemon) close() {
	d.stop()
	d.wg.Wait()
}

func withCommit(t *testing.T, commit string) {
	t.Helper()
	orig := buildinfo.Commit
	buildinfo.Commit = commit
	t.Cleanup(func() { buildinfo.Commit = orig })
}

func withDoNotSpawn(t *testing.T, v bool) {
	t.Helper()
	orig := doNotSpawn
	doNotSpawn = v
	t.Cleanup(func() { doNotSpawn = orig })
}

func ok(daemon.Cmd) *daemon.Resp { return &daemon.Resp{Success: true} }

func TestCompatErrorMessage(t *testing.T) {
	err := &compatError{daemonHash: "aaaaa", cliHash: "bbbbb"}
	want := "daemon version aaaaa not compatible with cli version bbbbb"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestSendCmdNoDaemon(t *testing.T) {
	withSocket(t)
	if _, err := sendCmd(daemon.Cmd{Kind: daemon.Nop}); err == nil {
		t.Fatal("expected error without daemon")
	}
}

func TestSendCmdNoResponse(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp { return nil })
	_, err := sendCmd(daemon.Cmd{Kind: daemon.Nop})
	if err == nil || !strings.Contains(err.Error(), "failed to read") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestSendCmdUnserializable(t *testing.T) {
	startFakeDaemon(t, ok)
	// time.Time cannot be marshaled to JSON outside of years 0-9999
	desc := &tunnel.Desc{Name: "t", LastConn: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	_, err := sendCmd(daemon.Cmd{Kind: daemon.Open, Tunnel: desc})
	if err == nil || !strings.Contains(err.Error(), "failed to serialize") {
		t.Fatalf("expected serialize error, got %v", err)
	}
}

func TestProbeDaemon(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp {
		return &daemon.Resp{Success: true, Info: daemon.Info{Commit: "aaaaa"}}
	})

	withCommit(t, "")
	if err := probeDaemon(); err != nil {
		t.Fatalf("expected no error without CLI commit, got %v", err)
	}

	buildinfo.Commit = "aaaaa"
	if err := probeDaemon(); err != nil {
		t.Fatalf("expected no error for matching commit, got %v", err)
	}

	buildinfo.Commit = "bbbbb"
	var ce *compatError
	if err := probeDaemon(); !errors.As(err, &ce) {
		t.Fatalf("expected compatError, got %v", err)
	}
}

func TestKillDaemonNoDaemon(t *testing.T) {
	withSocket(t)
	err := killDaemon(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not send shutdown command") {
		t.Fatalf("expected send error, got %v", err)
	}
}

func TestKillDaemonRefused(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp {
		return &daemon.Resp{Success: false, Error: "refused"}
	})
	err := killDaemon(context.Background())
	if err == nil || err.Error() != "refused" {
		t.Fatalf("expected 'refused', got %v", err)
	}
}

func TestKillDaemonSuccess(t *testing.T) {
	shutdown := make(chan struct{}, 1)
	d := startFakeDaemon(t, func(cmd daemon.Cmd) *daemon.Resp {
		if cmd.Kind == daemon.Shutdown {
			shutdown <- struct{}{}
		}
		return &daemon.Resp{Success: true}
	})
	// Release the socket once the shutdown command came in
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-shutdown
		d.stop()
	}()
	if err := killDaemon(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-stopped
}

func TestKillDaemonTimeout(t *testing.T) {
	// Daemon acknowledges shutdown but keeps its socket bound
	startFakeDaemon(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := killDaemon(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestEnsureDaemonRestartFails(t *testing.T) {
	startFakeDaemon(t, func(cmd daemon.Cmd) *daemon.Resp {
		if cmd.Kind == daemon.Shutdown {
			return &daemon.Resp{Success: false, Error: "refused"}
		}
		return &daemon.Resp{Success: true, Info: daemon.Info{Commit: "aaaaa"}}
	})
	withCommit(t, "bbbbb")
	err := ensureDaemon(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not restart daemon: refused") {
		t.Fatalf("expected restart error, got %v", err)
	}
}

func TestEnsureDaemonTimeout(t *testing.T) {
	// Launching works (the spawned test binary exits immediately), but
	// no daemon ever shows up on the socket.
	withSocket(t)
	withDoNotSpawn(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ensureDaemon(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestLaunchDaemonOSError(t *testing.T) {
	if _, err := launchDaemonOS(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for missing executable")
	}
}

func TestPrepareMissingConfigNonInteractive(t *testing.T) {
	startFakeDaemon(t, ok)
	withCommit(t, "")

	origTerm, origPath := isTerm, config.Path
	isTerm = false
	config.Path = filepath.Join(t.TempDir(), "missing.toml")
	t.Cleanup(func() { isTerm, config.Path = origTerm, origPath })

	conf, err := prepare()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf == nil || len(conf.Tunnels) != 0 {
		t.Fatalf("expected empty config, got %+v", conf)
	}
	if _, err := os.Stat(config.Path); !os.IsNotExist(err) {
		t.Fatalf("config file must not be created when not interactive")
	}
}
