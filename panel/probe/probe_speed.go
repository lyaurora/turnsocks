package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	testDownloadSourceName = "Cloudflare"
	testDownloadURL        = "https://speed.cloudflare.com/__down"
)

func measureDownloadSpeed(ctx context.Context, proxyAddr string, threads int, bytesEach int64) Speed {
	if threads <= 0 {
		threads = 1
	}
	start := time.Now()
	type partResult struct {
		bytes int64
		err   error
	}
	ch := make(chan partResult, threads)
	for i := 0; i < threads; i++ {
		go func() {
			n, err := downloadBytes(ctx, proxyAddr, bytesEach)
			ch <- partResult{bytes: n, err: err}
		}()
	}

	var total int64
	var firstErr error
	for i := 0; i < threads; i++ {
		part := <-ch
		total += part.bytes
		if firstErr == nil {
			firstErr = part.err
		}
	}
	return speedFromDownload(total, int64(threads)*bytesEach, time.Since(start), threads, firstErr)
}

func speedFromDownload(total, expected int64, elapsed time.Duration, threads int, downloadErr error) Speed {
	seconds := elapsed.Seconds()
	result := Speed{Bytes: total, Seconds: round2(seconds), Threads: threads, Source: testDownloadSourceName}
	if total > 0 && seconds > 0 {
		result.Mbps = round2(float64(total) * 8 / seconds / 1_000_000)
	}
	if downloadErr != nil {
		result.Message = "下载异常：" + downloadErr.Error()
		return result
	}
	if total != expected {
		result.Message = fmt.Sprintf("下载不完整：%d / %d 字节", total, expected)
		return result
	}
	result.OK = true
	result.Message = fmt.Sprintf("%.2f Mbps", result.Mbps)
	return result
}

func downloadBytes(ctx context.Context, proxyAddr string, size int64) (int64, error) {
	client := httpClientViaSOCKS(proxyAddr, 30*time.Second)
	defer client.CloseIdleConnections()
	targetURL, err := downloadURL(size)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
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

func downloadURL(size int64) (string, error) {
	u, err := url.Parse(testDownloadURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("bytes", strconv.FormatInt(size, 10))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
