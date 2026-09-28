package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alebeck/boring/internal/daemon"
	"github.com/alebeck/boring/internal/tunnel"
)

func TestOrderTunnelsForList(t *testing.T) {
	conf := []tunnel.Desc{{Name: "b"}, {Name: "a"}}
	running := map[string]*tunnel.Desc{
		"a": {Name: "a", Status: tunnel.Open},
		"z": {Name: "z", Status: tunnel.Open},
		"y": {Name: "y", Status: tunnel.Open},
	}
	all := orderTunnelsForList(conf, running)

	var names []string
	for _, d := range all {
		names = append(names, d.Name)
	}
	// Config order first, then extra running tunnels sorted by name
	if want := []string{"b", "a", "y", "z"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got order %v, want %v", names, want)
	}
	if all[1] != running["a"] {
		t.Fatalf("running tunnel should replace configured one")
	}
}

func TestGetRunningTunnelsNoDaemon(t *testing.T) {
	withSocket(t)
	if _, err := getRunningTunnels(); err == nil {
		t.Fatal("expected error without daemon")
	}
}

func TestGetRunningTunnelsFailure(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp {
		return &daemon.Resp{Success: false, Error: "list failed"}
	})
	if _, err := getRunningTunnels(); err == nil || err.Error() != "list failed" {
		t.Fatalf("expected 'list failed', got %v", err)
	}
}

func TestGetRunningTunnels(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp {
		return &daemon.Resp{Success: true, Tunnels: map[string]tunnel.Desc{
			"a": {Name: "a"}, "b": {Name: "b"},
		}}
	})
	ts, err := getRunningTunnels()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ts) != 2 || ts["a"].Name != "a" || ts["b"].Name != "b" {
		t.Fatalf("unexpected tunnels: %v", ts)
	}
}

func TestOpenTunnelNoDaemon(t *testing.T) {
	withSocket(t)
	if err := openTunnel(&tunnel.Desc{Name: "t"}); !errors.Is(err, errOpFailed) {
		t.Fatalf("expected errOpFailed, got %v", err)
	}
}

func TestOpenTunnelUnserializable(t *testing.T) {
	startFakeDaemon(t, ok)
	d := &tunnel.Desc{Name: "t", LastConn: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := openTunnel(d); !errors.Is(err, errOpFailed) {
		t.Fatalf("expected errOpFailed, got %v", err)
	}
}

func TestCloseTunnelNoDaemon(t *testing.T) {
	withSocket(t)
	if err := closeTunnel(&tunnel.Desc{Name: "t"}); !errors.Is(err, errOpFailed) {
		t.Fatalf("expected errOpFailed, got %v", err)
	}
}

func TestCloseTunnelFailure(t *testing.T) {
	startFakeDaemon(t, func(daemon.Cmd) *daemon.Resp {
		return &daemon.Resp{Success: false, Error: "tunnel not running"}
	})
	if err := closeTunnel(&tunnel.Desc{Name: "t"}); !errors.Is(err, errOpFailed) {
		t.Fatalf("expected errOpFailed, got %v", err)
	}
}
