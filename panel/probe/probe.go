package probe

import (
	"context"
	"time"
)

const (
	defaultDoH       = "https://cloudflare-dns.com/dns-query"
	testSingleBytes  = int64(32 * 1024 * 1024)
	testMultiBytes   = int64(16 * 1024 * 1024)
	testMultiThreads = 4
)

type Server struct {
	Raw  string
	Addr string
}

type Runner struct {
	ConfigPath string
}

type Result struct {
	OK            bool    `json:"ok"`
	Message       string  `json:"message"`
	Addr          string  `json:"addr"`
	TCPConnect    Metric  `json:"tcpConnect"`
	SOCKSUDP      Check   `json:"socksUdp"`
	SingleThread  Speed   `json:"singleThread"`
	MultiThread   Speed   `json:"multiThread"`
	Score         int     `json:"score"`
	DurationMS    float64 `json:"durationMs"`
	DownloadBytes int64   `json:"downloadBytes"`
	TestedAt      string  `json:"testedAt,omitempty"`
}

type Metric struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	AvgMS   float64 `json:"avgMs,omitempty"`
	MinMS   float64 `json:"minMs,omitempty"`
	MaxMS   float64 `json:"maxMs,omitempty"`
	Samples int     `json:"samples"`
	Failed  int     `json:"failed"`
}

type Check struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	MS      float64 `json:"ms,omitempty"`
}

type Speed struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	Mbps    float64 `json:"mbps,omitempty"`
	Bytes   int64   `json:"bytes,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Threads int     `json:"threads"`
	Source  string  `json:"source,omitempty"`
}

func (r Runner) Test(ctx context.Context, server Server, doh string) Result {
	start := time.Now()
	resp := Result{
		Addr:          server.Addr,
		DownloadBytes: testSingleBytes + int64(testMultiThreads)*testMultiBytes,
	}
	resp.TCPConnect = measureTCPConnect(server.Addr)

	proxyAddr, cleanup, err := r.startTestProxy(ctx, server.Raw, doh)
	if err != nil {
		msg := "临时代理启动失败：" + err.Error()
		resp.SOCKSUDP = Check{Message: msg}
		resp.SingleThread = Speed{Threads: 1, Message: msg}
		resp.MultiThread = Speed{Threads: testMultiThreads, Message: msg}
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
