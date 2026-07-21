package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

func TestServerNotesRoundTripAndFollowServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	raw := "LISTEN=127.0.0.1:1080\nTURN_SERVERS=first.example:3478,second.example:3478\nDOH=https://cloudflare-dns.com/dns-query\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := readProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerNotes = map[string]string{
		"first.example:3478":  "东京，家宽",
		"second.example:3478": "大阪备用",
	}
	if err := writeProxyConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := readProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	infos := buildServerInfo(
		[]string{"second.example:3478", "first.example:3478"},
		loaded.ServerNotes,
		"",
		nil,
	)
	if got := infos[0].Note; got != "大阪备用" {
		t.Fatalf("second server note = %q", got)
	}
	if got := infos[1].Note; got != "东京，家宽" {
		t.Fatalf("first server note = %q", got)
	}
}

func TestUpdateServerNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	raw := "LISTEN=127.0.0.1:1080\nTURN_SERVERS=turn.example:3478\nDOH=https://cloudflare-dns.com/dns-query\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	a := &app{configPath: path}
	req := httptest.NewRequest(http.MethodPost, "/api/servers/note", bytes.NewBufferString(`{"server":"turn.example:3478","note":"东京家宽"}`))
	res := httptest.NewRecorder()
	a.handleUpdateServerNote(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}

	cfg, err := readProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ServerNotes["turn.example:3478"]; got != "东京家宽" {
		t.Fatalf("note = %q", got)
	}
}
