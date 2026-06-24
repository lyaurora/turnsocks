package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func measureTCPConnect(addr string) Metric {
	const attempts = 4
	const timeout = 2 * time.Second
	const interval = 150 * time.Millisecond

	var samples []float64
	var lastErr error
	for i := 0; i < attempts; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			lastErr = err
		} else {
			_ = conn.Close()
			samples = append(samples, elapsedMS(start))
		}
		if i+1 < attempts {
			time.Sleep(interval)
		}
	}
	return metricFromSamples(samples, attempts, "TCP 连接失败", lastErr)
}

func (r Runner) startTestProxy(ctx context.Context, server string, doh string) (string, func(), error) {
	bin, err := findTurnsocksBinary(r.ConfigPath)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(doh) == "" {
		doh = defaultDoH
	}
	port, err := freeTCPPort()
	if err != nil {
		return "", nil, err
	}
	listen := "127.0.0.1:" + strconv.Itoa(port)
	statePath := filepath.Join(os.TempDir(), fmt.Sprintf("turnsocks-panel-test-%d.state", time.Now().UnixNano()))

	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, bin,
		"-listen", listen,
		"-turns", server,
		"-doh", doh,
		"-state", statePath,
		"-timeout", "8s",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cleanup := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		}
		_ = os.Remove(statePath)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			_ = os.Remove(statePath)
			return "", nil, fmt.Errorf("临时 turnsocks 已退出：%w", err)
		default:
		}
		conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return listen, cleanup, nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	cleanup()
	return "", nil, errors.New("临时 turnsocks 启动超时")
}

func findTurnsocksBinary(configPath string) (string, error) {
	var candidates []string
	if env := strings.TrimSpace(os.Getenv("TURNSOCKS_BIN")); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "turnsocks"))
	}
	if configPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(configPath), "turnsocks"))
	}
	candidates = append(candidates, "./turnsocks")
	if path, err := exec.LookPath("turnsocks"); err == nil {
		candidates = append(candidates, path)
	}
	for _, path := range candidates {
		if isExecutable(path) {
			return path, nil
		}
	}
	return "", errors.New("找不到 turnsocks 二进制")
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}

func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("无法分配临时端口")
	}
	return addr.Port, nil
}
