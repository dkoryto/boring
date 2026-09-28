package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAddr is a net.Addr with an arbitrary string form.
type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

// fakeConn is a net.Conn whose reads and local address can be controlled.
// Writes are collected in a buffer.
type fakeConn struct {
	net.Conn
	local   net.Addr
	readErr error
	block   <-chan struct{} // if set, Read waits for it to be closed

	mu  sync.Mutex
	out bytes.Buffer
}

func (c *fakeConn) Read([]byte) (int, error) {
	if c.block != nil {
		<-c.block
	}
	return 0, c.readErr
}
func (c *fakeConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(b)
}
func (c *fakeConn) Close() error        { return nil }
func (c *fakeConn) LocalAddr() net.Addr { return c.local }

func (c *fakeConn) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.out.Bytes()...)
}

// readResult is one scripted result of fakePacketConn.ReadFrom.
type readResult struct {
	data    []byte
	addr    net.Addr
	err     error
	wait    <-chan struct{} // if set, block until closed before returning
	started chan<- struct{} // if set, signalled when the read begins
}

// fakePacketConn is a net.PacketConn with scripted reads and writes.
// Once the script is exhausted, ReadFrom returns net.ErrClosed.
type fakePacketConn struct {
	net.PacketConn

	mu       sync.Mutex
	reads    []readResult
	writeN   func(n int) int
	writeErr error
	written  [][]byte
}

func (p *fakePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	p.mu.Lock()
	if len(p.reads) == 0 {
		p.mu.Unlock()
		return 0, nil, net.ErrClosed
	}
	r := p.reads[0]
	p.reads = p.reads[1:]
	p.mu.Unlock()
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.wait != nil {
		<-r.wait
	}
	return copy(b, r.data), r.addr, r.err
}

func (p *fakePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	p.written = append(p.written, append([]byte(nil), b...))
	if p.writeN != nil {
		return p.writeN(len(b)), nil
	}
	return len(b), nil
}

func (p *fakePacketConn) SetReadDeadline(time.Time) error { return nil }

// timeoutErr is what a read past its deadline returns; handleUDPRequest and
// handleUDPResponse wrap it once, which isTimeout expects.
func timeoutErr() error { return os.ErrDeadlineExceeded }

func udpPacket(t *testing.T, addr socksAddr, body string) []byte {
	pkt, err := (&udpRequest{addr: addr}).marshal()
	if err != nil {
		t.Fatal(err)
	}
	return append(pkt, body...)
}

func TestSocksAddrMarshalDomain(t *testing.T) {
	pkt, err := (socksAddr{addrType: domainName, addr: "example.com", port: 80}).marshal()
	if err != nil {
		t.Fatal(err)
	}
	a, err := parseSocksAddr(bytes.NewReader(pkt))
	if err != nil {
		t.Fatal(err)
	}
	if a.addrType != domainName || a.addr != "example.com" || a.port != 80 {
		t.Fatalf("got %+v", a)
	}
}

func handleTCPWithBackend(t *testing.T, backend net.Conn) (*fakeConn, error) {
	t.Helper()
	// The client never sends anything, so only the backend side can end
	// the relay. It is released once the test is over.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	client := &fakeConn{readErr: io.EOF, block: block}
	c := &Conn{
		srv: &Server{Dialer: func(context.Context, string, string) (net.Conn, error) {
			return backend, nil
		}},
		clientConn: client,
		request:    &request{command: connect, destination: socksAddr{addrType: ipv4, addr: "127.0.0.1", port: 1}},
	}
	return client, c.handleTCP()
}

func TestHandleTCPBadLocalAddr(t *testing.T) {
	client, err := handleTCPWithBackend(t, &fakeConn{local: fakeAddr("noport")})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(client.written()) != 0 {
		t.Fatalf("unexpected reply %v", client.written())
	}
}

func TestHandleTCPUnmarshalableBindAddr(t *testing.T) {
	backend := &fakeConn{local: fakeAddr(strings.Repeat("a", 256) + ":1"), readErr: io.EOF}
	client, err := handleTCPWithBackend(t, backend)
	if err != nil {
		t.Fatal(err)
	}
	if got := client.written(); len(got) < 2 || got[1] != byte(generalFailure) {
		t.Fatalf("got %v, want general failure", got)
	}
}

func TestHandleTCPBackendReadError(t *testing.T) {
	backend := &fakeConn{local: fakeAddr("127.0.0.1:1"), readErr: errors.New("boom")}
	client, err := handleTCPWithBackend(t, backend)
	if err == nil || !strings.Contains(err.Error(), "from backend to client") {
		t.Fatalf("got %v, want backend read error", err)
	}
	if got := client.written(); len(got) < 2 || got[1] != byte(success) {
		t.Fatalf("got %v, want success", got)
	}
}

func TestHandleUDPBadLocalAddr(t *testing.T) {
	c := &Conn{srv: &Server{}, clientConn: &fakeConn{local: fakeAddr("noport")}, request: &request{}}
	if err := c.handleUDP(); err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleUDPListenError(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation and not assigned locally,
	// so binding a UDP socket to it fails.
	client := &fakeConn{local: fakeAddr("192.0.2.1:1")}
	c := &Conn{srv: &Server{}, clientConn: client, request: &request{}}
	if err := c.handleUDP(); err == nil {
		t.Fatal("expected error")
	}
	if got := client.written(); len(got) < 2 || got[1] != byte(generalFailure) {
		t.Fatalf("got %v, want general failure", got)
	}
}

func TestHandleUDPRequestErrors(t *testing.T) {
	from := fakeAddr("127.0.0.1:1")
	good := udpPacket(t, socksAddr{addrType: ipv4, addr: "127.0.0.1", port: 9}, "data")

	cases := []struct {
		name   string
		pkt    []byte
		target *fakePacketConn
		want   string
	}{
		{"parse", []byte{0, 1, 0, byte(ipv4)}, &fakePacketConn{}, "parse udp request"},
		{"write", good, &fakePacketConn{writeErr: errors.New("boom")}, "write to target"},
		{"short write", good, &fakePacketConn{writeN: func(n int) int { return n - 1 }}, io.ErrShortWrite.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakePacketConn{reads: []readResult{{data: tc.pkt, addr: from}}}
			c := &Conn{}
			err := c.handleUDPRequest(client, tc.target, make([]byte, 1024), time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestHandleUDPRequestUnresolvableTarget(t *testing.T) {
	pkt := udpPacket(t, socksAddr{addrType: domainName, addr: "bad host", port: 9}, "data")
	client := &fakePacketConn{reads: []readResult{{data: pkt, addr: fakeAddr("127.0.0.1:1")}}}
	target := &fakePacketConn{}
	c := &Conn{}
	if err := c.handleUDPRequest(client, target, make([]byte, 1024), time.Second); err != nil {
		t.Fatal(err)
	}
	if len(target.written) != 1 || string(target.written[0]) != "data" {
		t.Fatalf("got %q", target.written)
	}
}

func TestHandleUDPResponseErrors(t *testing.T) {
	cases := []struct {
		name   string
		read   readResult
		client *fakePacketConn
		want   string
	}{
		{"read", readResult{err: errors.New("boom")}, &fakePacketConn{}, "read from target"},
		{"split", readResult{data: []byte("x"), addr: fakeAddr("noport")}, &fakePacketConn{}, "split host port"},
		{"marshal", readResult{data: []byte("x"), addr: fakeAddr(strings.Repeat("a", 256) + ":1")}, &fakePacketConn{}, "marshal udp request"},
		{"write", readResult{data: []byte("x"), addr: fakeAddr("127.0.0.1:1")}, &fakePacketConn{writeErr: errors.New("boom")}, "write to client"},
		{"short write", readResult{data: []byte("x"), addr: fakeAddr("127.0.0.1:1")}, &fakePacketConn{writeN: func(n int) int { return n - 1 }}, io.ErrShortWrite.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &fakePacketConn{reads: []readResult{tc.read}}
			c := &Conn{udpClientAddr: fakeAddr("127.0.0.1:2")}
			err := c.handleUDPResponse(target, tc.client, make([]byte, 1024), time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

// TestTransferUDPLoops drives both relay goroutines through a failed read
// and a timed out read, then ends the association and lets them stop.
func TestTransferUDPLoops(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	script := func() []readResult {
		return []readResult{
			{err: errors.New("boom")},
			{err: timeoutErr()},
			{err: timeoutErr(), wait: release, started: started},
		}
	}
	client := &fakePacketConn{reads: script()}
	target := &fakePacketConn{reads: script()}
	tcpDone := make(chan struct{})

	errc := make(chan error, 1)
	go func() {
		c := &Conn{}
		errc <- c.transferUDP(&fakeConn{readErr: io.EOF, block: tcpDone}, client, target)
	}()

	// Both goroutines have gone past the failed and the timed out read and
	// wait in the next one.
	<-started
	<-started
	close(tcpDone)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// transferUDP has returned, so its context is cancelled. The pending
	// reads now time out and the goroutines stop on ctx.Done.
	close(release)
}

func TestTransferUDPAssociatedConnError(t *testing.T) {
	client := &fakePacketConn{}
	target := &fakePacketConn{}
	c := &Conn{}
	err := c.transferUDP(&fakeConn{readErr: errors.New("boom")}, client, target)
	if err == nil || !strings.Contains(err.Error(), "udp associated tcp conn") {
		t.Fatalf("got %v", err)
	}
}
