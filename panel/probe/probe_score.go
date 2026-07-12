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

func scoreServerTest(resp Result) int {
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

func serverTestMessage(resp Result) string {
	if resp.SingleThread.OK || resp.MultiThread.OK {
		if !resp.SOCKSUDP.OK || !resp.SingleThread.OK || !resp.MultiThread.OK {
			return fmt.Sprintf("测试完成，部分项目失败：单线程 %.1f Mbps，多线程 %.1f Mbps，综合评分 %d", resp.SingleThread.Mbps, resp.MultiThread.Mbps, resp.Score)
		}
		return fmt.Sprintf("测试完成：单线程 %.1f Mbps，多线程 %.1f Mbps，综合评分 %d", resp.SingleThread.Mbps, resp.MultiThread.Mbps, resp.Score)
	}
	if resp.TCPConnect.OK {
		return fmt.Sprintf("测试失败：未测出可用带宽，TCP 延迟 %.1f ms，综合评分 %d", resp.TCPConnect.AvgMS, resp.Score)
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
