package probe

import (
	"context"
	"time"
)

const (
	defaultDoH       = "https://cloudflare-dns.com/dns-query"
	testSingleBytes  = int64(32 * 1024 * 1024)
	testMultiBytes   = int64(20 * 1024 * 1024)
	testMultiThreads = 4
)

type Mode string

const (
	ModeCheck Mode = "check"
	ModeSpeed Mode = "speed"
)

type Server struct {
	Raw  string
	Addr string
}

type Runner struct {
	ConfigPath string
}

type Result struct {
	Mode          Mode    `json:"mode,omitempty"`
	OK            bool    `json:"ok"`
	Message       string  `json:"message"`
	Addr          string  `json:"addr"`
	TCPConnect    Metric  `json:"tcpConnect"`
	SOCKSTCP      *Check  `json:"socksTcp,omitempty"`
	SOCKSUDP      Check   `json:"socksUdp"`
	SingleThread  Speed   `json:"singleThread"`
	MultiThread   Speed   `json:"multiThread"`
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

func (r Runner) Test(ctx context.Context, server Server, doh string, mode Mode) (resp Result) {
	start := time.Now()
	resp = Result{Addr: server.Addr, Mode: mode}
	defer func() {
		resp.DurationMS = elapsedMS(start)
		if ctx.Err() != nil {
			resp.OK = false
			resp.Message = "检测已停止"
		}
	}()
	if ctx.Err() != nil {
		return resp
	}
	if mode == ModeCheck {
		resp.TCPConnect = measureTCPConnect(ctx, server.Addr)
		if ctx.Err() != nil {
			return resp
		}
	}

	proxyAddr, cleanup, err := r.startTestProxy(ctx, server.Raw, doh)
	if err != nil {
		msg := "临时代理启动失败：" + err.Error()
		if mode == ModeCheck {
			resp.SOCKSUDP = Check{Message: msg}
		} else {
			resp.SingleThread = Speed{Threads: 1, Message: msg}
			resp.MultiThread = Speed{Threads: testMultiThreads, Message: msg}
		}
		resp.Message = msg
		return resp
	}
	defer cleanup()

	if mode == ModeCheck {
		tcp := testSOCKSTCP(ctx, proxyAddr)
		resp.SOCKSTCP = &tcp
		if ctx.Err() != nil {
			return resp
		}
		resp.SOCKSUDP = testSOCKSUDP(ctx, proxyAddr)
		resp.OK = tcp.OK && resp.SOCKSUDP.OK
		resp.Message = "检查完成，TCP / UDP 转发可用"
		if !resp.OK {
			resp.Message = "检查完成，部分转发不可用，请查看详情"
		}
		return resp
	}

	resp.DownloadBytes = testSingleBytes + int64(testMultiThreads)*testMultiBytes
	resp.SingleThread = measureDownloadSpeed(ctx, proxyAddr, 1, testSingleBytes)
	if ctx.Err() != nil {
		return resp
	}
	resp.MultiThread = measureDownloadSpeed(ctx, proxyAddr, testMultiThreads, testMultiBytes)
	resp.OK = resp.SingleThread.OK || resp.MultiThread.OK
	resp.Message = serverTestMessage(resp)
	return resp
}
