package proxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUDPSessionFailureClosesControlConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	s := &udpSession{clientTCP: server, closed: make(chan struct{})}
	s.fail()

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := client.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("control connection read error = %v, want EOF", err)
	}
}

func TestConnectedTurnAddrUsesControlPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := ln.Accept()
		accepted <- conn
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-accepted
	defer peer.Close()

	if got, want := connectedTurnAddr(conn, "fallback:3478"), conn.RemoteAddr().String(); got != want {
		t.Fatalf("connectedTurnAddr() = %q, want %q", got, want)
	}
}

func TestResolveDoHCachesDNSFailure(t *testing.T) {
	const host = "missing.example"
	dnsCache.Delete(host)
	defer dnsCache.Delete(host)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil || len(raw) < 2 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		response := make([]byte, 12)
		binary.BigEndian.PutUint16(response[0:2], binary.BigEndian.Uint16(raw[0:2]))
		response[2] = 0x81
		response[3] = 0x83
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(response)
	}))
	defer server.Close()

	cfg := Config{DoH: server.URL, DoHClient: server.Client(), DNSTTL: time.Minute, Timeout: time.Second}
	for i := 0; i < 2; i++ {
		if _, err := resolveDoH(host, cfg); err == nil {
			t.Fatal("resolveDoH() succeeded, want DNS error")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("DoH requests = %d, want 1", got)
	}
}
