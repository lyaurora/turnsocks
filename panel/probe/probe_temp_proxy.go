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

func measureTCPConnect(ctx context.Context, addr string) Metric {
	const attempts = 4
	const timeout = 2 * time.Second
	const interval = 150 * time.Millisecond

	var samples []float64
	var lastErr error
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return Metric{Message: ctx.Err().Error()}
		}
		start := time.Now()
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
		} else {
			_ = conn.Close()
			samples = append(samples, elapsedMS(start))
		}
		if i+1 < attempts {
			select {
			case <-ctx.Done():
				return Metric{Message: ctx.Err().Error()}
			case <-time.After(interval):
			}
		}
	}
	return metricFromSamples(samples, attempts, "TCP 连接失败", lastErr)
}

func (r Runner) startTestProxy(ctx context.Context, server string, doh string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
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
	tempDir, err := os.MkdirTemp("", "turnsocks-panel-test-")
	if err != nil {
		return "", nil, err
	}
	statePath := filepath.Join(tempDir, "state")
	configPath := filepath.Join(tempDir, "config.env")

	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, bin,
		"-config", configPath,
		"-listen", listen,
		"-turns", server,
		"-doh", doh,
		"-state", statePath,
		"-timeout", "8s",
	)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(tempDir)
		return "", nil, err
	}

	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	cleanup := func() {
		cancel()
		<-done
		_ = os.RemoveAll(tempDir)
	}

	startCtx, cancelStart := context.WithTimeout(ctx, 4*time.Second)
	defer cancelStart()
	for startCtx.Err() == nil {
		select {
		case <-done:
			cleanup()
			return "", nil, fmt.Errorf("临时 turnsocks 已退出：%v", waitErr)
		default:
		}
		conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(startCtx, "tcp", listen)
		if err == nil {
			_ = conn.Close()
			return listen, cleanup, nil
		}
		select {
		case <-startCtx.Done():
		case <-done:
		case <-time.After(120 * time.Millisecond):
		}
	}
	cleanup()
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
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
