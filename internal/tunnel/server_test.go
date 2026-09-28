package tunnel

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/log"
	"github.com/alebeck/boring/internal/ssh_config"
	"golang.org/x/crypto/ssh"
)

const keysDir = "../../test/testdata/keys/"

// logs collects everything the package logs during the tests, so that tests
// can wait for a message instead of sleeping.
var logs = &logSink{changed: make(chan struct{})}

func TestMain(m *testing.M) {
	log.Init(logs, true, false)
	os.Exit(m.Run())
}

type logSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.buf.Write(p)
	close(s.changed)
	s.changed = make(chan struct{})
	return n, err
}

// mark returns the current end of the log, to be passed to waitLog.
func (s *logSink) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

// waitLog waits until msg is logged somewhere after the given mark.
func waitLog(t *testing.T, mark int, msg string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		logs.mu.Lock()
		found := strings.Contains(logs.buf.String()[mark:], msg)
		changed := logs.changed
		logs.mu.Unlock()
		if found {
			return
		}
		select {
		case <-changed:
		case <-timeout:
			t.Fatalf("timed out waiting for log message %q", msg)
		}
	}
}

func readKey(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(keysDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signer(t *testing.T, name string) ssh.Signer {
	t.Helper()
	s, err := ssh.ParsePrivateKey(readKey(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testServer is a minimal SSH server which supports direct-tcpip channels,
// i.e. local port forwarding.
type testServer struct {
	ln    net.Listener
	port  int
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

func startServer(t *testing.T) *testServer {
	t.Helper()
	authorized, _, _, _, err := ssh.ParseAuthorizedKey(readKey(t, "client.pub"))
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(k.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorized")
		},
	}
	conf.AddHostKey(signer(t, "server"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{
		ln:    ln,
		port:  ln.Addr().(*net.TCPAddr).Port,
		conns: make(map[net.Conn]struct{}),
	}
	s.wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[conn] = struct{}{}
			s.mu.Unlock()
			s.wg.Go(func() { s.handle(conn, conf) })
		}
	})
	t.Cleanup(func() {
		s.dropConns(true)
		s.wg.Wait()
	})
	return s
}

func (s *testServer) handle(conn net.Conn, conf *ssh.ServerConfig) {
	defer conn.Close()
	_, chans, reqs, err := ssh.NewServerConn(conn, conf)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		var p struct {
			Addr       string
			Port       uint32
			OriginAddr string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		target, err := net.Dial("tcp", net.JoinHostPort(p.Addr, fmt.Sprint(p.Port)))
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		s.wg.Go(func() {
			defer ch.Close()
			defer target.Close()
			go func() {
				io.Copy(target, ch)
				target.Close()
			}()
			io.Copy(ch, target)
		})
	}
}

// hop returns a hop to the test server, authenticating with the test key
// unless noAuth is set.
func (s *testServer) hop(t *testing.T, noAuth bool) ssh_config.Hop {
	t.Helper()
	conf := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if !noAuth {
		conf.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer(t, "client"))}
	}
	return ssh_config.Hop{HostName: "127.0.0.1", Port: s.port, ClientConfig: conf}
}

// closedAddr returns a local TCP address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// echoServer returns the address of a TCP server that echoes back what it
// receives.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer c.Close()
				io.Copy(c, c)
			})
		}
	})
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return ln.Addr().String()
}

// dropConns closes all client connections to the server, while the server
// keeps accepting new ones unless stopAccepting is set.
func (s *testServer) dropConns(stopAccepting bool) {
	if stopAccepting {
		s.ln.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
		delete(s.conns, c)
	}
}
