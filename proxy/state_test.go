package proxy

import (
	"errors"
	"fmt"
	"net/url"
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

func TestConcurrentRuntimeStateWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := runtimestate.Update(path, func(state *runtimeState) {
				count := 0
				if state.LastFailure != nil {
					count, _ = strconv.Atoi(state.LastFailure.Message)
				}
				state.LastFailure = &runtimestate.Failure{Message: strconv.Itoa(count + 1)}
				state.Select(fmt.Sprintf("turn-%d:3478", i), "test selection")
			}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
	state := readRuntimeState(path)
	if state.LastFailure == nil || state.LastFailure.Message != "100" || state.LastSwitch == nil || state.LastSwitch.To != state.CurrentAddr {
		t.Fatalf("concurrent updates lost state: %+v", state)
	}
}

func TestDiagnosticsSurviveSwitchAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	servers := []turnServerConfig{
		{Addr: "turn-a:3478", Username: "test-user", Password: "test-secret", ExplicitAuth: true},
		{Addr: "turn-b:3478"},
	}
	p := newTurnPool(servers, time.Minute, path)
	p.markFailure(servers[0], fmt.Errorf("connect to %s failed", servers[0].String()))
	p.markSuccess(servers[1])
	state := readRuntimeState(path)
	if state.CurrentAddr != servers[1].Addr || state.LastFailure == nil || state.LastFailure.Addr != servers[0].Addr || state.LastFailure.Stage != "TURN TCP" {
		t.Fatalf("missing failure after failover: %+v", state)
	}
	if strings.Contains(state.LastFailure.Message, "test-secret") || strings.Contains(state.LastFailure.Message, "test-user") {
		t.Fatalf("diagnostics exposed credentials: %s", state.LastFailure.Message)
	}
	if state.LastSwitch == nil || state.LastSwitch.From != servers[0].Addr || state.LastSwitch.To != servers[1].Addr || !strings.Contains(state.LastSwitch.Reason, "失败") {
		t.Fatalf("missing failover reason: %+v", state.LastSwitch)
	}
	p = newTurnPool(servers, time.Minute, path)
	restarted := readRuntimeState(path)
	if p.current != servers[1].String() || !reflect.DeepEqual(restarted.LastSwitch, state.LastSwitch) || !reflect.DeepEqual(restarted.LastFailure, state.LastFailure) {
		t.Fatal("restart lost diagnostics or manufactured a new switch")
	}
	p.updateServers(servers[:1])
	updated := readRuntimeState(path)
	if updated.LastSwitch.To != servers[0].Addr || !strings.Contains(updated.LastSwitch.Reason, "移除") || !reflect.DeepEqual(updated.LastFailure, state.LastFailure) {
		t.Fatalf("node removal lost diagnostics: %+v", updated)
	}
}

func TestPanelSelectionWinsOverOlderProxyEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	servers := []turnServerConfig{{Addr: "turn-a:3478"}, {Addr: "turn-b:3478"}, {Addr: "turn-c:3478"}}
	p := newTurnPool(servers, time.Minute, path)
	if err := runtimestate.Update(path, func(state *runtimeState) { state.Select(servers[2].Addr, "面板手动切换") }); err != nil {
		t.Fatal(err)
	}
	p.markFailure(servers[0], errors.New("connection failed"))
	p.markSuccess(servers[1])
	state := readRuntimeState(path)
	if state.CurrentAddr != servers[2].Addr || state.LastSwitch.Reason != "面板手动切换" || state.LastFailure == nil {
		t.Fatalf("proxy replaced pending panel selection: %+v", state)
	}
}

func TestNodeStateCanRecoverAfterWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	servers := []turnServerConfig{{Addr: "turn-a:3478"}, {Addr: "turn-b:3478"}, {Addr: "turn-c:3478"}}
	p := newTurnPool(servers, time.Minute, path)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	p.markSuccess(servers[1])
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	p.markSuccess(servers[2])
	if state := readRuntimeState(path); state.CurrentAddr != servers[2].Addr {
		t.Fatalf("failed write was mistaken for a pending panel selection: %+v", state)
	}
}

func TestFailureDiagnosticsBoundWritesAndHideDoHURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	p := newTurnPool([]turnServerConfig{{Addr: "turn-a:3478"}}, time.Minute, path)
	p.recordFailure("", "DNS 解析", &url.Error{Op: "Post", URL: "https://dns.example/?token=private", Err: errors.New("connection refused")})
	first := readRuntimeState(path).LastFailure
	if first == nil || first.Message != "connection refused" {
		t.Fatalf("DoH diagnostics included its private URL: %+v", first)
	}
	p.recordFailure("", "DNS 解析", errors.New("another failure"))
	if !reflect.DeepEqual(readRuntimeState(path).LastFailure, first) {
		t.Fatal("failure burst was not rate limited")
	}
	p.lastFailureWrite = time.Time{}
	p.recordFailure("", "DNS 解析", errors.New(strings.Repeat("x\n", 500)))
	message := readRuntimeState(path).LastFailure.Message
	if len([]rune(message)) > 401 || strings.Contains(message, "\n") {
		t.Fatal("unbounded or multiline diagnostic message")
	}
}
