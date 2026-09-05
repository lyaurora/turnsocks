package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyaurora/turnsocks/runtimestate"
)

func TestReadConfigDuplicateValues(test *testing.T) {
	path := filepath.Join(test.TempDir(), "config.env")
	content := " # comment=ignored\n\n LISTEN = '127.0.0.1:9999'\nLISTEN=\n" +
		"DOH=https://dns.example/query\nDOH=\nTURN_SERVERS=turn.example:3478\nTURN_SERVERS=\n" +
		"PANEL_USERNAME=first\n PANEL_USERNAME = 'last'\nPANEL_PASSWORD=first\nPANEL_PASSWORD='pa=ss'\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		test.Fatal(err)
	}
	cfg, err := readProxyConfig(path)
	if err != nil {
		test.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9999" || cfg.DoH != "https://dns.example/query" || len(cfg.Servers) != 0 || cfg.PanelUsername != "last" || cfg.PanelPassword != "pa=ss" {
		test.Fatalf("duplicate config values changed: %+v", cfg)
	}
}

func TestPanelPasswordRoundTrip(t *testing.T) {
	for _, password := range []string{"plain", "demo-password'", "'quoted'", "\"quoted\"", "pa\\ss\"'", "a\\n\\t$pass"} {
		t.Run(password, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.env")
			if err := os.WriteFile(path, []byte("LISTEN=127.0.0.1:1080\nDOH=https://cloudflare-dns.com/dns-query\nTURN_SERVERS=\n"), 0600); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(configRequest{
				Listen: defaultProxyListen, DoH: defaultDoH,
				PanelAuthEnabled: true, PanelUsername: "admin", PanelPassword: password,
			})
			if err != nil {
				t.Fatal(err)
			}
			a := &app{configPath: path}
			rec := httptest.NewRecorder()
			a.handleUpdateConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config/update", bytes.NewReader(body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("save failed: %s", rec.Body.String())
			}
			auth, _, err := loadPanelAuth(path)
			if err != nil || !auth.valid("admin", password) {
				t.Fatalf("saved password cannot log in: %v", err)
			}
		})
	}
}

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

func TestAddServerRejectsInvalidInputWithoutChangingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	original := []byte("LISTEN=127.0.0.1:1080\nTURN_SERVERS=good.example:3478\nDOH=https://cloudflare-dns.com/dns-query\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{configPath: path}
	for _, server := range []string{"user:@turn.example:3478", "turn,example:3478", "turn.example\nPANEL_PASSWORD=demo:3478"} {
		body, err := json.Marshal(serverRequest{Server: server})
		if err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		a.handleAddServer(res, httptest.NewRequest(http.MethodPost, "/api/servers/add", bytes.NewReader(body)))
		if res.Code != http.StatusBadRequest {
			t.Fatalf("invalid node accepted: %q, status=%d", server, res.Code)
		}
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("rejected node changed config: %v", err)
		}
	}
}

func TestConfigApplyRollback(t *testing.T) {
	for _, operation := range []string{"settings", "select"} {
		for _, tc := range []struct {
			mode        string
			wantCalls   int
			wantMessage string
		}{
			{mode: "success", wantCalls: 1, wantMessage: "已重启"},
			{mode: "restart_failure", wantCalls: 2, wantMessage: "已恢复旧配置，代理已恢复"},
			{mode: "startup_failure", wantCalls: 2, wantMessage: "已恢复旧配置，代理已恢复"},
			{mode: "recovery_failure", wantCalls: 2, wantMessage: "代理恢复失败"},
			{mode: "config_restore_failure", wantCalls: 1, wantMessage: "旧配置恢复失败"},
			{mode: "state_restore_failure", wantCalls: 2, wantMessage: "节点状态恢复失败"},
			{mode: "state_write_failure", wantCalls: 1, wantMessage: "节点状态恢复失败"},
		} {
			if operation != "select" && tc.mode == "state_write_failure" {
				continue
			}
			t.Run(operation+"/"+tc.mode, func(t *testing.T) {
				dir := t.TempDir()
				a := &app{configPath: filepath.Join(dir, "config.env"), statePath: filepath.Join(dir, "state")}
				oldListener := readinessListener(t, "127.0.0.1:0", true)
				newListener := readinessListener(t, "127.0.0.1:0", tc.mode != "startup_failure")
				if tc.mode != "success" && tc.mode != "startup_failure" {
					_ = newListener.Close()
				}
				before := proxyConfig{
					Listen: oldListener.Addr().String(), DoH: defaultDoH,
					Servers:       []string{"first.example:3478", "second.example:3478", "third.example:3478"},
					ServerNotes:   map[string]string{"first.example:3478": "personal node"},
					PanelUsername: "old-user", PanelPassword: "old'password",
				}
				custom := "# personal settings\nCUSTOM='keep this'\n"
				if err := os.WriteFile(a.configPath, []byte(updateProxyConfigText(custom, before)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := writeRuntimeState(a.statePath, "second.example:3478"); err != nil {
					t.Fatal(err)
				}
				if err := runtimestate.Update(a.statePath, func(state *runtimeState) {
					state.LastFailure = &runtimestate.Failure{Stage: "TURN TCP", Message: "previous failure"}
				}); err != nil {
					t.Fatal(err)
				}
				previousRuntime := readRuntimeState(a.statePath)
				if tc.mode == "state_write_failure" {
					if err := os.Remove(a.statePath); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(a.statePath, 0700); err != nil {
						t.Fatal(err)
					}
				}
				// PATH contains only these stubs: the tests cannot restart real services.
				t.Setenv("PATH", dir)
				t.Setenv("CONFIG_TEST_MODE", tc.mode)
				t.Setenv("CONFIG_TEST_CONFIG", a.configPath)
				t.Setenv("CONFIG_TEST_STATE", a.statePath)
				t.Setenv("CONFIG_TEST_CALLS", filepath.Join(dir, "calls"))
				t.Setenv("CONFIG_TEST_FIRST", filepath.Join(dir, "first-restart"))
				t.Setenv("CONFIG_TEST_PID", strconv.Itoa(os.Getpid()))
				t.Setenv("CONFIG_TEST_OTHER_PID", strconv.Itoa(os.Getppid()))
				sudo := `#!/bin/sh
set -eu
[ "$*" = '-n systemctl restart turnsocks' ] || exit 99
printf '%s\n' "$*" >> "$CONFIG_TEST_CALLS"
if [ -f "$CONFIG_TEST_FIRST" ]; then
    /bin/cp "$CONFIG_TEST_CONFIG" "$CONFIG_TEST_CONFIG.recovery"
    [ "$CONFIG_TEST_MODE" != recovery_failure ]
    exit
fi
: > "$CONFIG_TEST_FIRST"
case "$CONFIG_TEST_MODE" in
    success|startup_failure|state_write_failure) exit 0 ;;
    config_restore_failure) /bin/mkdir "$CONFIG_TEST_CONFIG.tmp" ;;
    state_restore_failure) /bin/rm "$CONFIG_TEST_STATE"; /bin/mkdir "$CONFIG_TEST_STATE" ;;
esac
exit 1
`
				for name, script := range map[string]string{
					"sudo":      sudo,
					"systemctl": readinessSystemctl,
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
				}

				next := before
				var request any
				handler := a.handleUpdateConfig
				if operation == "select" {
					next.Servers = []string{"third.example:3478", "first.example:3478", "second.example:3478"}
					request = serverRequest{Server: "third.example:3478"}
					handler = a.handleSelectServer
				} else {
					next.Listen = newListener.Addr().String()
					next.PanelUsername, next.PanelPassword = "new-user", "new-password"
					request = configRequest{
						Listen: next.Listen, DoH: next.DoH, PanelAuthEnabled: true,
						PanelUsername: next.PanelUsername, PanelPassword: next.PanelPassword,
					}
				}
				body, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				res := httptest.NewRecorder()
				handler(res, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
				if (res.Code == http.StatusOK) != (tc.mode == "success") || !strings.Contains(res.Body.String(), tc.wantMessage) {
					t.Errorf("status = %d, body = %s; want %q", res.Code, res.Body.String(), tc.wantMessage)
				}
				wantConfig := before
				wantCurrent := "second.example:3478"
				if tc.mode == "success" || tc.mode == "config_restore_failure" {
					wantConfig = next
					if operation == "select" {
						wantCurrent = "third.example:3478"
					}
				}
				if tc.mode == "state_restore_failure" || tc.mode == "state_write_failure" {
					wantCurrent = ""
				}
				got, err := readProxyConfig(a.configPath)
				if err != nil || !reflect.DeepEqual(got, wantConfig) {
					t.Errorf("config after apply = %#v, %v; want %#v", got, err, wantConfig)
				}
				if got := readRuntimeState(a.statePath).CurrentAddr; got != wantCurrent {
					t.Errorf("current node = %q, want %q", got, wantCurrent)
				}
				if tc.mode == "restart_failure" || tc.mode == "startup_failure" || tc.mode == "recovery_failure" {
					state := readRuntimeState(a.statePath)
					if !reflect.DeepEqual(state.LastSwitch, previousRuntime.LastSwitch) || !reflect.DeepEqual(state.LastFailure, previousRuntime.LastFailure) {
						t.Errorf("rollback lost failure history or kept a failed selection: %+v", state)
					}
				}
				raw, err := os.ReadFile(a.configPath)
				if err != nil || !strings.Contains(string(raw), custom) {
					t.Errorf("custom config was lost: %s, %v", raw, err)
				}
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				if got := strings.Count(string(calls), "\n"); got != tc.wantCalls {
					t.Errorf("restart calls = %d, want %d", got, tc.wantCalls)
				}
				if tc.wantCalls == 2 {
					recovered, err := readProxyConfig(a.configPath + ".recovery")
					if err != nil || !reflect.DeepEqual(recovered, before) {
						t.Errorf("old config was not restored before recovery restart: %#v, %v", recovered, err)
					}
				}
			})
		}
	}
}

// Both supported commands model a Type=simple process that can fail after the
// restart job succeeds. The recovery restart uses this test process's listener.
const readinessSystemctl = `#!/bin/sh
active=active
pid=$CONFIG_TEST_PID
if [ "$CONFIG_TEST_MODE" = startup_failure ] && [ ! -f "$CONFIG_TEST_CONFIG.recovery" ]; then
    if [ -f "$CONFIG_TEST_CONFIG.checked" ]; then active=failed; pid=0; fi
    if [ "$active" = active ]; then pid=$CONFIG_TEST_OTHER_PID; fi
    : > "$CONFIG_TEST_CONFIG.checked"
fi
case "$*" in
    'is-active turnsocks') printf '%s\n' "$active"; [ "$active" = active ] ;;
    'show --property=ActiveState,MainPID turnsocks') printf 'ActiveState=%s\nMainPID=%s\n' "$active" "$pid" ;;
    *) exit 99 ;;
esac
`

func readinessListener(t *testing.T, addr string, socks bool) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(conn, greeting); err != nil {
					return
				}
				if !bytes.Equal(greeting, []byte{5, 1, 0}) {
					t.Errorf("unexpected SOCKS greeting %v", greeting)
					return
				}
				if socks {
					_, _ = conn.Write([]byte{5, 0})
				} else {
					_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				}
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		workers.Wait()
	})
	return listener
}

func TestReadinessChecksSOCKSAndProcess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listen  string
		socks   bool
		foreign bool
		exits   bool
		wantOK  bool
	}{
		{name: "own IPv4 SOCKS", listen: "127.0.0.1:0", socks: true, wantOK: true},
		{name: "own IPv6 SOCKS", listen: "[::1]:0", socks: true, wantOK: true},
		{name: "dual stack IPv4 SOCKS", listen: "[::]:0", socks: true, wantOK: true},
		{name: "HTTP listener", listen: "127.0.0.1:0"},
		{name: "another process SOCKS", listen: "127.0.0.1:0", socks: true, foreign: true},
		{name: "service exits after handshake", listen: "127.0.0.1:0", socks: true, exits: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener := readinessListener(t, tc.listen, tc.socks)
			addr := listener.Addr().String()
			if tc.listen == "[::]:0" {
				addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
			}
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("CONFIG_TEST_MODE", "success")
			pid := os.Getpid()
			if tc.foreign {
				pid = os.Getppid()
			}
			t.Setenv("CONFIG_TEST_PID", strconv.Itoa(pid))
			if tc.exits {
				t.Setenv("CONFIG_TEST_MODE", "startup_failure")
				t.Setenv("CONFIG_TEST_CONFIG", filepath.Join(dir, "config.env"))
				t.Setenv("CONFIG_TEST_OTHER_PID", strconv.Itoa(pid))
			}
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(readinessSystemctl), 0700); err != nil {
				t.Fatal(err)
			}
			if err := waitTurnsocksReady(500*time.Millisecond, addr); (err == nil) != tc.wantOK {
				t.Fatalf("readiness error = %v, want ready %v", err, tc.wantOK)
			}
		})
	}
}
