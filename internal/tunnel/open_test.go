package tunnel

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/ssh_config"
	"golang.org/x/crypto/ssh"
)

// preparedTunnel returns a local forwarding tunnel through s which skips
// the SSH config lookup done by prepare.
func preparedTunnel(t *testing.T, s *testServer, local, remote string) *Tunnel {
	keepAlive := 0
	return &Tunnel{
		prepared:   true,
		hops:       []ssh_config.Hop{s.hop(t, false)},
		localAddr:  &address{local, "tcp"},
		remoteAddr: &address{remote, "tcp"},
		Desc:       &Desc{Name: t.Name(), Mode: Local, KeepAlive: &keepAlive},
	}
}

func openOrFail(t *testing.T, tun *Tunnel) {
	t.Helper()
	if err := tun.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeAndWait(t, tun) })
}

func closeAndWait(t *testing.T, tun *Tunnel) {
	t.Helper()
	if tun.Snapshot().Status == Closed {
		return
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-tun.Closed:
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not close")
	}
}

func expectErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("expected error containing %q, got %v", msg, err)
	}
}

func TestOpenNoHops(t *testing.T) {
	tun := &Tunnel{prepared: true, Desc: &Desc{Name: "test"}}
	expectErr(t, tun.Open(), "no connections specified")
}

func TestOpenCannotListen(t *testing.T) {
	s := startServer(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	tun := preparedTunnel(t, s, busy.Addr().String(), closedAddr(t))
	expectErr(t, tun.Open(), "cannot listen")
	// The client must have been closed again
	tun.wg.Wait()
}

func TestOpenJumpDialError(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", closedAddr(t))
	_, port, _ := net.SplitHostPort(closedAddr(t))
	unreachable := s.hop(t, false)
	unreachable.Port, _ = strconv.Atoi(port)
	tun.hops = append(tun.hops, unreachable)

	expectErr(t, tun.Open(), "could not connect to host")
	tun.wg.Wait()
}

func TestOpenJumpHandshakeError(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", closedAddr(t))
	tun.hops = append(tun.hops, s.hop(t, true))

	expectErr(t, tun.Open(), "unable to authenticate")
	tun.wg.Wait()
}

func TestForward(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", echoServer(t))
	openOrFail(t, tun)

	conn, err := net.Dial("tcp", tun.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q, want %q", buf, "ping")
	}
}

func TestForwardDialError(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", closedAddr(t))
	openOrFail(t, tun)

	mark := logs.mark()
	conn, err := net.Dial("tcp", tun.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitLog(t, mark, "could not dial")
}

func TestReconnect(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", closedAddr(t))
	openOrFail(t, tun)

	mark := logs.mark()
	s.dropConns(false)
	waitLog(t, mark, "try re-connect")
	waitLog(t, mark, "opened tunnel")
}

func TestReconnectInterrupted(t *testing.T) {
	s := startServer(t)
	tun := preparedTunnel(t, s, "127.0.0.1:0", closedAddr(t))
	openOrFail(t, tun)

	mark := logs.mark()
	s.dropConns(true)
	waitLog(t, mark, "Retrying in")
	closeAndWait(t, tun)
	waitLog(t, mark, "interrupted by stop signal")
}

func TestKeepAliveDisabled(t *testing.T) {
	zero := 0
	tun := &Tunnel{Desc: &Desc{Name: "test", KeepAlive: &zero}}
	// Returns right away without touching the (nil) client
	tun.keepAlive(make(chan struct{}))
}

func TestKeepAliveError(t *testing.T) {
	s := startServer(t)
	h := s.hop(t, false)
	c, err := ssh.Dial("tcp", net.JoinHostPort(h.HostName, strconv.Itoa(h.Port)), h.ClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	one := 1
	tun := &Tunnel{Desc: &Desc{Name: "test", KeepAlive: &one}, client: c}
	mark := logs.mark()
	// Returns once the keep-alive fails, since cancel is never closed
	tun.keepAlive(make(chan struct{}))
	waitLog(t, mark, "error sending keepalive")
}
