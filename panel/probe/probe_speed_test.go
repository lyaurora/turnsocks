package probe

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDownloadSpeedRequiresCompleteTransfer(t *testing.T) {
	const expected = int64(1 << 20)
	for _, tc := range []struct {
		name  string
		bytes int64
		err   error
		ok    bool
	}{
		{"complete", expected, nil, true},
		{"missing one byte", expected - 1, nil, false},
		{"reset after 99 percent", expected * 99 / 100, io.ErrUnexpectedEOF, false},
		{"90 percent is incomplete", expected * 90 / 100, nil, false},
		{"error with full byte count", expected, errors.New("connection reset"), false},
		{"no data", 0, errors.New("connection refused"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := speedFromDownload(tc.bytes, expected, time.Second, 1, tc.err)
			if got.OK != tc.ok {
				t.Fatalf("OK = %v, want %v: %s", got.OK, tc.ok, got.Message)
			}
			if got.Bytes != tc.bytes || got.Seconds != 1 || got.Threads != 1 || got.Source != testDownloadSourceName {
				t.Fatalf("download measurements changed: %+v", got)
			}
			if tc.bytes > 0 && got.Mbps <= 0 || tc.bytes == 0 && got.Mbps != 0 {
				t.Fatalf("measured throughput lost or invented: %+v", got)
			}
			if tc.err != nil && !strings.Contains(got.Message, tc.err.Error()) {
				t.Fatalf("download error was hidden: %s", got.Message)
			}
		})
	}
}

func TestIncompleteDownloadSummaryKeepsMeasuredSpeed(t *testing.T) {
	speed := speedFromDownload(1<<20, 2<<20, time.Second, 1, io.ErrUnexpectedEOF)
	result := Result{TCPConnect: Metric{OK: true}, SingleThread: speed, MultiThread: speed}
	message := serverTestMessage(result)
	if !strings.Contains(message, "测试未完成") || !strings.Contains(message, "8.4 Mbps") {
		t.Fatalf("partial measurements were hidden: %s", message)
	}
	result.SingleThread = Speed{}
	result.MultiThread = Speed{}
	if message := serverTestMessage(result); !strings.Contains(message, "未测出可用带宽") {
		t.Fatalf("empty results should not imply measured throughput: %s", message)
	}
}
