package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRuntimeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	if err := writeRuntimeState(path, "turn.example.com:3478"); err != nil {
		t.Fatal(err)
	}

	state := readRuntimeState(path)
	if state.CurrentAddr != "turn.example.com:3478" {
		t.Fatalf("got current addr %q", state.CurrentAddr)
	}
	if state.UpdatedAt == "" {
		t.Fatal("missing updated_at")
	}
}

func TestLocalCheckAddr(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:1080": "127.0.0.1:1080",
		"0.0.0.0:1080":   "127.0.0.1:1080",
		":1080":          "127.0.0.1:1080",
		"[::]:1080":      "[::1]:1080",
	}
	for input, want := range tests {
		if got := localCheckAddr(input); got != want {
			t.Errorf("localCheckAddr(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteProxyConfigAllowsEmptyServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte("LISTEN=127.0.0.1:1080\nTURN_SERVERS=old.example:3478\nDOH=https://cloudflare-dns.com/dns-query\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := proxyConfig{Listen: defaultProxyListen, DoH: defaultDoH}
	if err := writeProxyConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "TURN_SERVERS=\n") {
		t.Fatalf("empty TURN_SERVERS was not written:\n%s", raw)
	}
	loaded, err := readProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != 0 {
		t.Fatalf("got %d TURN servers, want 0", len(loaded.Servers))
	}
}
