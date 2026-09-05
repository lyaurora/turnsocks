package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lyaurora/turnsocks/panel/probe"
)

func TestProbeModesKeepSeparateResults(t *testing.T) {
	dir := t.TempDir()
	a := &app{testPath: filepath.Join(dir, "tests.json"), checkPath: filepath.Join(dir, "checks.json")}
	server := "turn.example:3478"
	speed := probe.Result{Mode: probe.ModeSpeed, OK: true, Message: "previous speed", SingleThread: probe.Speed{OK: true, Mbps: 100}}
	if err := a.saveServerTest(server, speed); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.testPath)
	if err != nil {
		t.Fatal(err)
	}
	check := probe.Result{Mode: probe.ModeCheck, OK: true, Message: "new check", SOCKSTCP: &probe.Check{OK: true}}
	if err := a.saveServerTest(server, check); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(a.testPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("light check replaced speed results: %s, %v", after, err)
	}
	infos := buildServerInfo([]string{server}, nil, server, a.readServerTests(a.testPath), a.readServerTests(a.checkPath))
	if infos[0].Test == nil || infos[0].Test.SingleThread.Mbps != 100 || infos[0].Check == nil || infos[0].Check.Message != check.Message {
		t.Fatalf("missing separate results: %+v", infos[0])
	}
	speed.SingleThread.Mbps = 200
	if err := a.saveServerTest(server, speed); err != nil {
		t.Fatal(err)
	}
	if got := a.readServerTests(a.checkPath)[server]; got.Message != check.Message || got.SOCKSTCP == nil || !got.SOCKSTCP.OK {
		t.Fatalf("speed test replaced connectivity results: %+v", got)
	}
	a.deleteServerTest(server)
	if len(a.readServerTests(a.testPath)) != 0 || len(a.readServerTests(a.checkPath)) != 0 {
		t.Fatal("deleting a node left probe records")
	}
}

func TestProbeCancellationAndConcurrentRequests(t *testing.T) {
	dir := t.TempDir()
	a := &app{configPath: filepath.Join(dir, "config.env"), testPath: filepath.Join(dir, "tests.json"), checkPath: filepath.Join(dir, "checks.json")}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	server := ln.Addr().String()
	if err := os.WriteFile(a.configPath, []byte("TURN_SERVERS="+server+",second.example:3478\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []probe.Mode{probe.ModeSpeed, probe.ModeCheck} {
		if err := a.saveServerTest(server, probe.Result{Mode: mode, OK: true, Message: "previous result"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(serverRequest{Server: server, Mode: probe.ModeCheck})
	req := httptest.NewRequest(http.MethodPost, "/api/servers/test", bytes.NewReader(body)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.handleServerTest(httptest.NewRecorder(), req)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("canceled handler is still running")
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	for _, mode := range []probe.Mode{probe.ModeCheck, probe.ModeSpeed, "unknown"} {
		body, _ := json.Marshal(serverRequest{Server: "second.example:3478", Mode: mode})
		res := httptest.NewRecorder()
		a.handleServerTest(res, httptest.NewRequest(http.MethodPost, "/api/servers/test", bytes.NewReader(body)))
		want := http.StatusConflict
		if mode == "unknown" {
			want = http.StatusBadRequest
		}
		if res.Code != want {
			t.Fatalf("concurrent/invalid %q probe status = %d, body = %s", mode, res.Code, res.Body.String())
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCP probe cancellation was delayed")
	}
	res := httptest.NewRecorder()
	a.handleServerTest(res, httptest.NewRequest(http.MethodPost, "/api/servers/test", bytes.NewReader(body)).WithContext(ctx))
	if res.Code != http.StatusOK {
		t.Fatalf("completed cancellation did not release the probe slot: %s", res.Body.String())
	}
	for _, path := range []string{a.testPath, a.checkPath} {
		if got := a.readServerTests(path)[server]; !got.OK || got.Message != "previous result" {
			t.Fatalf("cancellation replaced stored result: %+v", got)
		}
	}
}
