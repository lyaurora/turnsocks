package probe

import (
	"fmt"
	"math"
	"time"
)

func metricFromSamples(samples []float64, attempts int, failMessage string, lastErr error) Metric {
	m := Metric{Samples: len(samples), Failed: attempts - len(samples)}
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

func serverTestMessage(resp Result) string {
	if resp.SingleThread.OK || resp.MultiThread.OK {
		if !resp.SOCKSUDP.OK || !resp.SingleThread.OK || !resp.MultiThread.OK {
			return fmt.Sprintf("测试完成，部分项目失败：单线程 %.1f Mbps，多线程 %.1f Mbps", resp.SingleThread.Mbps, resp.MultiThread.Mbps)
		}
		return fmt.Sprintf("测试完成：单线程 %.1f Mbps，多线程 %.1f Mbps", resp.SingleThread.Mbps, resp.MultiThread.Mbps)
	}
	if resp.TCPConnect.OK {
		return fmt.Sprintf("测试失败：未测出可用带宽，TCP 延迟 %.1f ms", resp.TCPConnect.AvgMS)
	}
	return "测试失败"
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
