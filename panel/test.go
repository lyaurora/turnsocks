package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	testDownloadURL  = "https://cachefly.cachefly.net/50mb.test"
	testSingleBytes  = int64(32 * 1024 * 1024)
	testMultiBytes   = int64(16 * 1024 * 1024)
	testMultiThreads = 4
)

type serverTestResponse struct {
	OK            bool        `json:"ok"`
	Message       string      `json:"message"`
	Addr          string      `json:"addr"`
	TCPConnect    testMetric  `json:"tcpConnect"`
	SOCKSUDP      testCheck   `json:"socksUdp"`
	SingleThread  speedMetric `json:"singleThread"`
	MultiThread   speedMetric `json:"multiThread"`
	Score         int         `json:"score"`
	DurationMS    float64     `json:"durationMs"`
	DownloadBytes int64       `json:"downloadBytes"`
	TestedAt      string      `json:"testedAt,omitempty"`
}

type testMetric struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	AvgMS   float64 `json:"avgMs,omitempty"`
	MinMS   float64 `json:"minMs,omitempty"`
	MaxMS   float64 `json:"maxMs,omitempty"`
	Samples int     `json:"samples"`
	Failed  int     `json:"failed"`
}

type testCheck struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	MS      float64 `json:"ms,omitempty"`
}

type speedMetric struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	Mbps    float64 `json:"mbps,omitempty"`
	Bytes   int64   `json:"bytes,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Threads int     `json:"threads"`
}

func (a *app) readServerTests() map[string]serverTestResponse {
	a.testMu.Lock()
	defer a.testMu.Unlock()
	tests, _ := readServerTestsFile(a.testPath)
	return tests
}

func (a *app) saveServerTest(server string, result serverTestResponse) error {
	normalized, err := normalizeServer(server)
	if err != nil {
		return err
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	tests, err := readServerTestsFile(a.testPath)
	if err != nil {
		return err
	}
	tests[normalized] = result
	return writeServerTestsFile(a.testPath, tests)
}

func (a *app) deleteServerTest(server string) {
	normalized, err := normalizeServer(server)
	if err != nil {
		return
	}

	a.testMu.Lock()
	defer a.testMu.Unlock()

	tests, err := readServerTestsFile(a.testPath)
	if err != nil {
		return
	}
	if _, ok := tests[normalized]; !ok {
		return
	}
	delete(tests, normalized)
	_ = writeServerTestsFile(a.testPath, tests)
}

func readServerTestsFile(path string) (map[string]serverTestResponse, error) {
	tests := make(map[string]serverTestResponse)
	if path == "" {
		return tests, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tests, nil
		}
		return tests, err
	}
	if len(raw) == 0 {
		return tests, nil
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		return tests, err
	}
	return tests, nil
}

func writeServerTestsFile(path string, tests map[string]serverTestResponse) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(tests, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *app) testServer(ctx context.Context, server string, info serverInfo, doh string) serverTestResponse {
	start := time.Now()
	resp := serverTestResponse{
		Addr:          info.Addr,
		DownloadBytes: testSingleBytes + int64(testMultiThreads)*testMultiBytes,
	}
	resp.TCPConnect = measureTCPConnect(info.Addr)

	proxyAddr, cleanup, err := a.startTestProxy(ctx, server, doh)
	if err != nil {
		msg := "临时代理启动失败：" + err.Error()
		resp.SOCKSUDP = testCheck{Message: msg}
		resp.SingleThread = speedMetric{Threads: 1, Message: msg}
		resp.MultiThread = speedMetric{Threads: testMultiThreads, Message: msg}
		resp.Score = scoreServerTest(resp)
		resp.DurationMS = round1(float64(time.Since(start).Microseconds()) / 1000)
		resp.Message = msg
		return resp
	}
	defer cleanup()

	resp.SOCKSUDP = testSOCKSUDP(proxyAddr)
	resp.SingleThread = measureDownloadSpeed(ctx, proxyAddr, 1, testSingleBytes)
	resp.MultiThread = measureDownloadSpeed(ctx, proxyAddr, testMultiThreads, testMultiBytes)
	resp.OK = resp.SingleThread.OK || resp.MultiThread.OK
	resp.Score = scoreServerTest(resp)
	resp.DurationMS = round1(float64(time.Since(start).Microseconds()) / 1000)
	resp.Message = serverTestMessage(resp)
	return resp
}

func measureTCPConnect(addr string) testMetric {
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

func (a *app) startTestProxy(ctx context.Context, server string, doh string) (string, func(), error) {
	bin, err := findTurnsocksBinary(a.configPath)
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

func measureDownloadSpeed(ctx context.Context, proxyAddr string, threads int, bytesEach int64) speedMetric {
	if threads <= 0 {
		threads = 1
	}
	result := speedMetric{Threads: threads}
	start := time.Now()
	type partResult struct {
		bytes int64
		err   error
	}
	ch := make(chan partResult, threads)
	for i := 0; i < threads; i++ {
		offset := int64(i) * bytesEach
		go func() {
			n, err := downloadBytes(ctx, proxyAddr, testDownloadURL, offset, bytesEach)
			ch <- partResult{bytes: n, err: err}
		}()
	}

	var total int64
	var failed []string
	for i := 0; i < threads; i++ {
		part := <-ch
		total += part.bytes
		if part.err != nil {
			failed = append(failed, part.err.Error())
		}
	}
	seconds := time.Since(start).Seconds()
	result.Bytes = total
	result.Seconds = round2(seconds)
	if total > 0 && seconds > 0 {
		result.Mbps = round2(float64(total) * 8 / seconds / 1_000_000)
	}
	expected := int64(threads) * bytesEach
	if total < expected*90/100 {
		if len(failed) > 0 {
			result.Message = failed[0]
		} else {
			result.Message = fmt.Sprintf("下载数据不足：%.1fMB / %.1fMB", float64(total)/1024/1024, float64(expected)/1024/1024)
		}
		return result
	}
	if len(failed) > 0 {
		if total < expected*95/100 {
			result.Message = fmt.Sprintf("部分完成 %.2f Mbps", result.Mbps)
		}
	} else if total < expected*95/100 {
		result.Message = fmt.Sprintf("部分完成 %.2f Mbps", result.Mbps)
	}
	result.OK = true
	if result.Message == "" {
		result.Message = fmt.Sprintf("%.2f Mbps", result.Mbps)
	}
	return result
}

func downloadBytes(ctx context.Context, proxyAddr, targetURL string, offset int64, size int64) (int64, error) {
	client := httpClientViaSOCKS(proxyAddr, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.Copy(io.Discard, io.LimitReader(resp.Body, size))
}

func httpClientViaSOCKS(proxyAddr string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           socks5DialContext(proxyAddr, 8*time.Second),
		DisableKeepAlives:     true,
		DisableCompression:    true,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func socks5DialContext(proxyAddr string, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		if network != "tcp" {
			return nil, fmt.Errorf("unsupported network %s", network)
		}
		dialer := net.Dialer{Timeout: timeout}
		conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		if err := socks5Connect(conn, addr, timeout); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

func socks5Connect(conn net.Conn, target string, timeout time.Duration) error {
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	buf := make([]byte, 260)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return errors.New("SOCKS5 no-auth 被拒绝")
	}

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return fmt.Errorf("invalid target port %q", port)
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		ip4 := ip.To4()
		if ip4 == nil {
			return errors.New("IPv6 target is not supported")
		}
		req = append(req, 0x01)
		req = append(req, ip4...)
	} else {
		if len(host) > 255 {
			return errors.New("target host too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(portNum>>8), byte(portNum))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return err
	}
	if buf[0] != 0x05 {
		return errors.New("invalid SOCKS5 response")
	}
	if buf[1] != 0x00 {
		return fmt.Errorf("SOCKS5 connect failed: 0x%02x", buf[1])
	}
	return readSOCKS5Bind(conn, buf[3])
}

func readSOCKS5Bind(conn net.Conn, atyp byte) error {
	var skip int
	switch atyp {
	case 0x01:
		skip = 4
	case 0x03:
		ln := []byte{0}
		if _, err := io.ReadFull(conn, ln); err != nil {
			return err
		}
		skip = int(ln[0])
	case 0x04:
		skip = 16
	default:
		return fmt.Errorf("unsupported SOCKS5 bind atyp 0x%02x", atyp)
	}
	rest := make([]byte, skip+2)
	_, err := io.ReadFull(conn, rest)
	return err
}

func testSOCKSUDP(proxyAddr string) testCheck {
	start := time.Now()
	tcpConn, udpAddr, err := socks5UDPAssociate(proxyAddr, 5*time.Second)
	if err != nil {
		return testCheck{Message: err.Error()}
	}
	defer tcpConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return testCheck{Message: err.Error()}
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(8 * time.Second))

	txID, payload := dnsQueryPayload("cloudflare.com")
	packet := buildSOCKSUDPDatagram(net.ParseIP("1.1.1.1"), 53, payload)
	if _, err := udpConn.WriteToUDP(packet, udpAddr); err != nil {
		return testCheck{Message: err.Error()}
	}

	buf := make([]byte, 4096)
	n, _, err := udpConn.ReadFromUDP(buf)
	if err != nil {
		return testCheck{Message: err.Error()}
	}
	dnsPayload, err := parseSOCKSUDPDatagram(buf[:n])
	if err != nil {
		return testCheck{Message: err.Error()}
	}
	if len(dnsPayload) < 2 || binary.BigEndian.Uint16(dnsPayload[:2]) != txID {
		return testCheck{Message: "DNS 响应不匹配"}
	}
	return testCheck{OK: true, Message: "UDP 转发可用", MS: elapsedMS(start)}
}

func socks5UDPAssociate(proxyAddr string, timeout time.Duration) (net.Conn, *net.UDPAddr, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	buf := make([]byte, 260)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		_ = conn.Close()
		return nil, nil, errors.New("SOCKS5 no-auth 被拒绝")
	}
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if buf[1] != 0x00 {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("UDP ASSOCIATE failed: 0x%02x", buf[1])
	}
	host, err := readSOCKS5Addr(conn, buf[3])
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	port := int(binary.BigEndian.Uint16(buf[:2]))
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, udpAddr, nil
}

func readSOCKS5Addr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 0x03:
		ln := []byte{0}
		if _, err := io.ReadFull(conn, ln); err != nil {
			return "", err
		}
		buf := make([]byte, int(ln[0]))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 addr atyp 0x%02x", atyp)
	}
}

func dnsQueryPayload(name string) (uint16, []byte) {
	txID := uint16(time.Now().UnixNano())
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], txID)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	for _, part := range strings.Split(name, ".") {
		msg = append(msg, byte(len(part)))
		msg = append(msg, part...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return txID, msg
}

func buildSOCKSUDPDatagram(ip net.IP, port int, payload []byte) []byte {
	ip4 := ip.To4()
	packet := []byte{0, 0, 0, 1}
	packet = append(packet, ip4...)
	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	return append(packet, payload...)
}

func parseSOCKSUDPDatagram(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[2] != 0 {
		return nil, errors.New("invalid UDP relay response")
	}
	offset := 4
	switch packet[3] {
	case 0x01:
		offset += 4
	case 0x03:
		if len(packet) <= offset {
			return nil, errors.New("invalid UDP relay domain response")
		}
		offset += 1 + int(packet[offset])
	case 0x04:
		offset += 16
	default:
		return nil, fmt.Errorf("unsupported UDP relay atyp 0x%02x", packet[3])
	}
	offset += 2
	if len(packet) < offset {
		return nil, errors.New("short UDP relay response")
	}
	return packet[offset:], nil
}

func metricFromSamples(samples []float64, attempts int, failMessage string, lastErr error) testMetric {
	m := testMetric{Samples: len(samples), Failed: attempts - len(samples)}
	if len(samples) == 0 {
		m.Message = failMessage
		if lastErr != nil {
			m.Message += "：" + lastErr.Error()
		}
		return m
	}
	minMS, maxMS, sum := samples[0], samples[0], 0.0
	for _, ms := range samples {
		if ms < minMS {
			minMS = ms
		}
		if ms > maxMS {
			maxMS = ms
		}
		sum += ms
	}
	m.OK = true
	m.AvgMS = round1(sum / float64(len(samples)))
	m.MinMS = round1(minMS)
	m.MaxMS = round1(maxMS)
	m.Message = fmt.Sprintf("%.1f ms", m.AvgMS)
	return m
}

func scoreServerTest(resp serverTestResponse) int {
	score := 0
	if resp.TCPConnect.OK {
		score += clampInt(20-int(resp.TCPConnect.AvgMS/5), 0, 20)
	}
	if resp.SOCKSUDP.OK {
		score += 15
	}
	if resp.SingleThread.OK {
		score += clampInt(int(resp.SingleThread.Mbps/3), 0, 40)
	}
	if resp.MultiThread.OK {
		score += clampInt(int(resp.MultiThread.Mbps/8), 0, 25)
	}
	return clampInt(score, 0, 100)
}

func serverTestMessage(resp serverTestResponse) string {
	if resp.SingleThread.OK || resp.MultiThread.OK {
		if !resp.SOCKSUDP.OK || !resp.SingleThread.OK || !resp.MultiThread.OK {
			return fmt.Sprintf("测试完成，部分项目失败：单线 %.1f Mbps，多线 %.1f Mbps，评分 %d", resp.SingleThread.Mbps, resp.MultiThread.Mbps, resp.Score)
		}
		return fmt.Sprintf("测试完成：单线 %.1f Mbps，多线 %.1f Mbps，评分 %d", resp.SingleThread.Mbps, resp.MultiThread.Mbps, resp.Score)
	}
	if resp.TCPConnect.OK {
		return fmt.Sprintf("测试失败：未测出可用带宽，TCP %.1f ms，评分 %d", resp.TCPConnect.AvgMS, resp.Score)
	}
	return "测试失败"
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func elapsedMS(start time.Time) float64 {
	return round1(float64(time.Since(start).Microseconds()) / 1000)
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
