package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

func readServiceInfo() serviceInfo {
	active := strings.TrimSpace(commandOutput(2*time.Second, "systemctl", "is-active", serviceName)) == "active"
	pid := strings.TrimSpace(commandOutput(2*time.Second, "pgrep", "-x", serviceName))
	if idx := strings.IndexByte(pid, '\n'); idx >= 0 {
		pid = pid[:idx]
	}
	return serviceInfo{Active: active, PID: pid}
}

func restartTurnsocks(listen string) error {
	if err := runCommand(10*time.Second, "sudo", "-n", "systemctl", "restart", serviceName); err != nil {
		return fmt.Errorf("重启 %s 失败：%w", serviceName, err)
	}
	return waitTurnsocksReady(8*time.Second, listen)
}

func waitTurnsocksReady(timeout time.Duration, listen string) error {
	deadline := time.Now().Add(timeout)
	checkAddr := localCheckAddr(listen)
	for time.Now().Before(deadline) {
		if strings.TrimSpace(commandOutput(2*time.Second, "systemctl", "is-active", serviceName)) == "active" {
			conn, err := net.DialTimeout("tcp", checkAddr, 500*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s 未在 %s 恢复监听", serviceName, checkAddr)
}

func localCheckAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		if ip != nil && ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, port)
}

func runCommand(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Run()
}

func commandOutput(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeAPIError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, apiResponse{OK: false, Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
