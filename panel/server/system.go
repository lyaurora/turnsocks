package server

import (
	"context"
	"encoding/json"
	"fmt"
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

func restartTurnsocks() error {
	if err := runCommand(10*time.Second, "sudo", "-n", "systemctl", "restart", serviceName); err != nil {
		return fmt.Errorf("重启 %s 失败：%w", serviceName, err)
	}
	return waitTurnsocksReady(8 * time.Second)
}

func waitTurnsocksReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.TrimSpace(commandOutput(2*time.Second, "systemctl", "is-active", serviceName)) == "active" {
			return nil
		}
		if strings.TrimSpace(commandOutput(2*time.Second, "pgrep", "-x", serviceName)) != "" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s 未恢复运行", serviceName)
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
