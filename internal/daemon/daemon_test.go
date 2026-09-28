package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/tunnel"
)

func testDaemon(t *testing.T) *daemon {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &daemon{
		ctx:     ctx,
		cancel:  cancel,
		tunnels: make(map[string]*tunnel.Tunnel),
		opening: make(map[string]chan struct{}),
	}
}

func TestRemoveTunnelKeepsNewer(t *testing.T) {
	d := testDaemon(t)
	old := tunnel.FromDesc(&tunnel.Desc{Name: "a"})
	cur := tunnel.FromDesc(&tunnel.Desc{Name: "a"})
	d.tunnels["a"] = cur

	d.removeTunnel(old)
	if d.tunnels["a"] != cur {
		t.Fatal("removing an old tunnel dropped the one that replaced it")
	}

	d.removeTunnel(cur)
	if _, ok := d.tunnels["a"]; ok {
		t.Fatal("tunnel not removed")
	}
}

func TestReserveRunning(t *testing.T) {
	d := testDaemon(t)
	d.tunnels["a"] = tunnel.FromDesc(&tunnel.Desc{Name: "a"})
	if err := d.reserve("a"); !errors.Is(err, AlreadyRunning) {
		t.Fatalf("expected %v, got %v", AlreadyRunning, err)
	}
}

// finishOpen mimics the end of openTunnel.
func finishOpen(d *daemon, name string, ok bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	close(d.opening[name])
	delete(d.opening, name)
	if ok {
		d.tunnels[name] = tunnel.FromDesc(&tunnel.Desc{Name: name})
	}
}

func reserveAsync(d *daemon, name string) chan error {
	res := make(chan error, 1)
	go func() { res <- d.reserve(name) }()
	return res
}

func expectBlocked(t *testing.T, res chan error) {
	t.Helper()
	select {
	case err := <-res:
		t.Fatalf("reserve returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func expectResult(t *testing.T, res chan error, want error) {
	t.Helper()
	select {
	case err := <-res:
		if !errors.Is(err, want) {
			t.Fatalf("expected %v, got %v", want, err)
		}
	case <-time.After(time.Second):
		t.Fatal("reserve did not return")
	}
}

func TestReserveWaitsForSuccessfulOpen(t *testing.T) {
	d := testDaemon(t)
	if err := d.reserve("a"); err != nil {
		t.Fatal(err)
	}
	res := reserveAsync(d, "a")
	expectBlocked(t, res)
	finishOpen(d, "a", true)
	expectResult(t, res, AlreadyRunning)
}

func TestReserveRetriesAfterFailedOpen(t *testing.T) {
	d := testDaemon(t)
	if err := d.reserve("a"); err != nil {
		t.Fatal(err)
	}
	res := reserveAsync(d, "a")
	expectBlocked(t, res)
	finishOpen(d, "a", false)
	expectResult(t, res, nil)
	if _, ok := d.opening["a"]; !ok {
		t.Fatal("second reserve did not take over the name")
	}
}

func TestReserveShutdown(t *testing.T) {
	d := testDaemon(t)
	if err := d.reserve("a"); err != nil {
		t.Fatal(err)
	}
	res := reserveAsync(d, "a")
	expectBlocked(t, res)
	d.cancel()
	expectResult(t, res, context.Canceled)
}
