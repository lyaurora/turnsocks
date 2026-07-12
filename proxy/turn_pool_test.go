package proxy

import (
	"errors"
	"testing"
	"time"
)

func TestUDPMarksDoNotChangeCurrent(t *testing.T) {
	servers := []turnServerConfig{{Addr: "turn-a:3478"}, {Addr: "turn-b:3478"}}
	p := newTurnPool(servers, time.Minute, "")
	p.markSuccess(servers[0])

	p.markUDPFailure(servers[0], errors.New("udp failed"))
	if got := p.candidates()[0]; got.String() != servers[0].String() {
		t.Fatalf("UDP failure changed current candidate to %q", got.String())
	}

	p.markUDPSuccess(servers[1])
	if p.current != servers[0].String() {
		t.Fatalf("UDP success changed current to %q", p.current)
	}
	if got := p.candidates()[0]; got.String() != servers[0].String() {
		t.Fatalf("UDP marks changed TCP candidates: got %q", got.String())
	}

	p.markFailure(servers[0], errors.New("tcp failed"))
	if got := p.candidates()[0]; got.String() != servers[1].String() {
		t.Fatalf("TCP failure did not move to next candidate: got %q", got.String())
	}
}

func TestUDPTrafficCanPromoteCurrent(t *testing.T) {
	servers := []turnServerConfig{{Addr: "turn-a:3478"}, {Addr: "turn-b:3478"}}
	p := newTurnPool(servers, time.Minute, "")
	p.markSuccess(servers[0])

	s := &udpSession{cfg: Config{TurnPool: p}, turn: servers[1]}
	s.markUDPTraffic()

	if p.current != servers[1].String() {
		t.Fatalf("UDP traffic did not promote current: got %q", p.current)
	}
}

func TestInitialTurnServerPrefersRuntimeState(t *testing.T) {
	servers := []turnServerConfig{{Addr: "turn-a:3478"}, {Addr: "turn-b:3478"}}

	got := initialTurnServer(servers, runtimeState{CurrentAddr: "turn-b:3478"})
	if got.String() != servers[1].String() {
		t.Fatalf("got %q, want %q", got.String(), servers[1].String())
	}

	got = initialTurnServer(servers, runtimeState{CurrentAddr: "missing:3478"})
	if got.String() != servers[0].String() {
		t.Fatalf("got %q, want fallback %q", got.String(), servers[0].String())
	}
}

func TestTurnPoolAddsFirstServer(t *testing.T) {
	p := newTurnPool(nil, time.Minute, "")
	if got := p.candidates(); len(got) != 0 {
		t.Fatalf("empty pool returned %d candidates", len(got))
	}

	server := turnServerConfig{Addr: "turn-a:3478"}
	changed, currentChanged, currentAddr, added, removed := p.updateServers([]turnServerConfig{server})
	if !changed || !currentChanged || currentAddr != server.Addr || added != 1 || removed != 0 {
		t.Fatalf("unexpected update result: changed=%v currentChanged=%v currentAddr=%q added=%d removed=%d", changed, currentChanged, currentAddr, added, removed)
	}
	if got := p.candidates(); len(got) != 1 || got[0].String() != server.String() {
		t.Fatalf("first server was not activated: %#v", got)
	}
}
