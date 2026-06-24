package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

func measureDownloadSpeed(ctx context.Context, proxyAddr string, threads int, bytesEach int64) Speed {
	if threads <= 0 {
		threads = 1
	}
	result := Speed{Threads: threads}
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
