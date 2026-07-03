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

	p.markUDPSuccess(servers[1])
	if p.current != servers[0].String() {
		t.Fatalf("UDP success changed current to %q", p.current)
	}

	p.markUDPFailure(servers[0], errors.New("udp failed"))
	if got := p.candidates()[0]; got.String() != servers[0].String() {
		t.Fatalf("UDP failure removed current from candidates: got %q", got.String())
	}

	p.markFailure(servers[0], errors.New("tcp failed"))
	if got := p.candidates()[0]; got.String() != servers[1].String() {
		t.Fatalf("TCP failure did not move to next candidate: got %q", got.String())
	}
}
