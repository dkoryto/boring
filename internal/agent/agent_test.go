package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// resetInst clears the cached agent client before and after a test.
func resetInst(t *testing.T) {
	t.Helper()
	mu.Lock()
	inst = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		inst = nil
		mu.Unlock()
	})
}

// sockPath returns a short unix socket path; macOS limits these to 104 bytes,
// which t.TempDir() can exceed.
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// listen starts a unix listener and hands each accepted connection to handle.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	p := sockPath(t)
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-done
	})
	return p
}

func TestGetSignersNoSocket(t *testing.T) {
	resetInst(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	if _, err := GetSigners(); err == nil {
		t.Fatal("expected error when SSH_AUTH_SOCK is unset")
	}
}

func TestGetSignersDialError(t *testing.T) {
	resetInst(t)
	t.Setenv("SSH_AUTH_SOCK", sockPath(t))

	if _, err := GetSigners(); err == nil {
		t.Fatal("expected error when agent socket does not exist")
	}
}

func TestGetSignersKeyring(t *testing.T) {
	resetInst(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kr := agent.NewKeyring()
	if err := kr.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	p := listen(t, func(c net.Conn) {
		defer c.Close()
		_ = agent.ServeAgent(kr, c)
	})
	t.Setenv("SSH_AUTH_SOCK", p)

	sigs, err := GetSigners()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 {
		t.Fatalf("got %d signers, want 1", len(sigs))
	}

	// The client is cached, so a broken SSH_AUTH_SOCK must not matter now.
	t.Setenv("SSH_AUTH_SOCK", "")
	sigs, err = GetSigners()
	if err != nil {
		t.Fatalf("cached agent: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("cached agent: got %d signers, want 1", len(sigs))
	}
}

func TestGetSignersAgentError(t *testing.T) {
	resetInst(t)

	// An agent that hangs up immediately makes the List request fail.
	p := listen(t, func(c net.Conn) { c.Close() })
	t.Setenv("SSH_AUTH_SOCK", p)

	if _, err := GetSigners(); err == nil {
		t.Fatal("expected error from agent that closes the connection")
	}
}
