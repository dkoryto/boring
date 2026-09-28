package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/ipc"
	"github.com/alebeck/boring/internal/tunnel"
)

func TestMain(m *testing.M) {
	// Unix socket paths are limited to ~100 bytes, so keep them short
	dir, err := os.MkdirTemp("", "dt")
	if err != nil {
		panic(err)
	}
	initLogging(filepath.Join(dir, logFileName))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// startDaemon runs a daemon serving on a fresh unix socket and returns the
// socket path.
func startDaemon(t *testing.T) (*daemon, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, sockName)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d, cleanup := newDaemon(ctx, ln)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serve()
		cleanup()
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return d, sock
}

func send(t *testing.T, sock string, cmd Cmd) Resp {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := ipc.Write(cmd, conn); err != nil {
		t.Fatal(err)
	}
	var resp Resp
	if err := ipc.Read(&resp, conn); err != nil {
		t.Fatal(err)
	}
	return resp
}

func expectFailure(t *testing.T, resp Resp, msg string) {
	t.Helper()
	if resp.Success || !strings.Contains(resp.Error, msg) {
		t.Fatalf("expected failure containing %q, got %+v", msg, resp)
	}
}

func TestHandleCommands(t *testing.T) {
	d, sock := startDaemon(t)

	if resp := send(t, sock, Cmd{Kind: Nop}); !resp.Success {
		t.Fatalf("nop failed: %+v", resp)
	}
	expectFailure(t, send(t, sock, Cmd{Kind: Open}), "no tunnel specified")
	expectFailure(t, send(t, sock, Cmd{Kind: Close}), "no tunnel specified")
	expectFailure(t, send(t, sock, Cmd{Kind: CmdKind(42)}), "unknown command: 42")
	expectFailure(t, send(t, sock, Cmd{Kind: Close, Tunnel: &tunnel.Desc{Name: "a"}}),
		"tunnel not running")
	// A tunnel without a host fails to open
	expectFailure(t, send(t, sock, Cmd{Kind: Open, Tunnel: &tunnel.Desc{Name: "a"}}), "")

	d.mutex.Lock()
	b := tunnel.FromDesc(&tunnel.Desc{Name: "b", Status: tunnel.Open})
	d.tunnels["b"] = b
	d.mutex.Unlock()
	resp := send(t, sock, Cmd{Kind: List})
	// Forget the tunnel again, the daemon's cleanup would wait for it to close
	d.removeTunnel(b)
	if !resp.Success || resp.Tunnels["b"].Status != tunnel.Open || len(resp.Tunnels) != 1 {
		t.Fatalf("unexpected list response: %+v", resp)
	}

	if resp := send(t, sock, Cmd{Kind: Shutdown}); !resp.Success {
		t.Fatalf("shutdown failed: %+v", resp)
	}
	select {
	case <-d.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestCloseClosedTunnel(t *testing.T) {
	d, sock := startDaemon(t)
	d.mutex.Lock()
	a := tunnel.FromDesc(&tunnel.Desc{Name: "a", Status: tunnel.Closed})
	d.tunnels["a"] = a
	d.mutex.Unlock()
	// Forget the tunnel again, the daemon's cleanup would wait for it to close
	defer d.removeTunnel(a)
	expectFailure(t, send(t, sock, Cmd{Kind: Close, Tunnel: &tunnel.Desc{Name: "a"}}),
		"trying to close a closed tunnel")
}

func TestHandleBadCommand(t *testing.T) {
	d := testDaemon(t)
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleConn(server)
	}()
	if _, err := client.Write([]byte("not json\n")); err != nil {
		t.Fatal(err)
	}
	// The daemon hangs up without answering
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected connection to be closed")
	}
	<-done
}

func TestRespondError(t *testing.T) {
	client, server := net.Pipe()
	client.Close()
	// Must not panic or block when the client is gone
	respond(server, nil, nil)
}

// flakyListener fails the first Accept with a transient error and reports
// the listener as closed afterwards.
type flakyListener struct {
	net.Listener
	mu    sync.Mutex
	calls int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls == 1 {
		return nil, errors.New("transient")
	}
	return nil, net.ErrClosed
}

func TestServeAcceptError(t *testing.T) {
	d := testDaemon(t)
	ln := &flakyListener{}
	d.ln = ln
	d.serve()
	if ln.calls != 2 {
		t.Fatalf("expected serve to retry once, got %d accepts", ln.calls)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "dt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	orig := Socket
	t.Cleanup(func() { Socket = orig })
	Socket = filepath.Join(dir, sockName)

	// Leave a socket file behind that nobody listens on, as happens when
	// the daemon is killed
	stale, err := net.Listen("unix", Socket)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	ln, err := listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln.Close()
}
