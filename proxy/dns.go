package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type dnsEntry struct {
	IP       net.IP
	ExpireAt time.Time
}

type dnsLookupResult struct {
	IP  net.IP
	Err error
}

type dnsLookupCall struct {
	done   chan struct{}
	result dnsLookupResult
}

var (
	dnsCache    sync.Map
	dnsLookupMu sync.Mutex
	dnsLookups  = make(map[string]*dnsLookupCall)
)

func resolveDoH(host string, cfg Config) (net.IP, error) {
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4, nil
		}
		return nil, errors.New("IPv6 is not supported")
	}

	queryHost := normalizeDNSHost(host)
	if queryHost == "" {
		return nil, errors.New("empty DNS host")
	}

	if v, ok := dnsCache.Load(queryHost); ok {
		e := v.(dnsEntry)
		if time.Now().Before(e.ExpireAt) {
			return e.IP, nil
		}
		dnsCache.Delete(queryHost)
	}

	return resolveDoHOnce(queryHost, cfg)
}

func resolveDoHOnce(queryHost string, cfg Config) (net.IP, error) {
	dnsLookupMu.Lock()
	if call := dnsLookups[queryHost]; call != nil {
		dnsLookupMu.Unlock()
		<-call.done
		return call.result.IP, call.result.Err
	}
	call := &dnsLookupCall{done: make(chan struct{})}
	dnsLookups[queryHost] = call
	dnsLookupMu.Unlock()

	ip, err := queryDoH(queryHost, cfg)
	call.result = dnsLookupResult{IP: ip, Err: err}

	dnsLookupMu.Lock()
	delete(dnsLookups, queryHost)
	close(call.done)
	dnsLookupMu.Unlock()

	return ip, err
}

func queryDoH(queryHost string, cfg Config) (net.IP, error) {
	u, err := buildDoHURL(cfg.DoH)
	if err != nil {
		return nil, err
	}
	query, queryID, err := buildDNSAQuery(queryHost)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("POST", u, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/dns-message")
	httpReq.Header.Set("Content-Type", "application/dns-message")

	httpClient := cfg.DoHClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DoH HTTP status %s", resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	ip, ttl, err := parseDNSAResponse(raw, queryID, queryHost, cfg.DNSTTL)
	if err != nil {
		return nil, err
	}
	dnsCache.Store(queryHost, dnsEntry{IP: ip, ExpireAt: time.Now().Add(ttl)})
	return ip, nil
}

func normalizeDNSHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func cleanupDNSCache(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		dnsCache.Range(func(key, value any) bool {
			entry, ok := value.(dnsEntry)
			if ok && now.After(entry.ExpireAt) {
				dnsCache.Delete(key)
			}
			return true
		})
	}
}

func buildDoHURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	normalizeWireDoHEndpoint(u)
	return u.String(), nil
}

func normalizeWireDoHEndpoint(u *url.URL) {
	if strings.EqualFold(u.Hostname(), "dns.google") && strings.TrimRight(u.EscapedPath(), "/") == "/resolve" {
		u.Path = "/dns-query"
		u.RawPath = ""
	}
}

func buildDNSAQuery(host string) ([]byte, uint16, error) {
	id := uint16(time.Now().UnixNano())
	msg := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	name, err := encodeDNSName(host)
	if err != nil {
		return nil, 0, err
	}
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return msg, id, nil
}

func encodeDNSName(host string) ([]byte, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, errors.New("empty DNS host")
	}
	var out []byte
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return nil, fmt.Errorf("invalid DNS host %q", host)
		}
		if len(label) > 63 {
			return nil, fmt.Errorf("DNS label too long in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	if len(out) > 254 {
		return nil, fmt.Errorf("DNS host too long %q", host)
	}
	out = append(out, 0)
	return out, nil
}

func parseDNSAResponse(msg []byte, wantID uint16, host string, maxTTL time.Duration) (net.IP, time.Duration, error) {
	if len(msg) < 12 {
		return nil, 0, errors.New("short DNS response")
	}
	if gotID := binary.BigEndian.Uint16(msg[0:2]); gotID != wantID {
		return nil, 0, errors.New("DNS response ID mismatch")
	}
	if msg[3]&0x0f != 0 {
		return nil, 0, fmt.Errorf("DNS response code %d", msg[3]&0x0f)
	}
	questions := int(binary.BigEndian.Uint16(msg[4:6]))
	answers := int(binary.BigEndian.Uint16(msg[6:8]))
	offset := 12
	var err error
	for i := 0; i < questions; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+4 {
			return nil, 0, errors.New("short DNS question")
		}
		offset += 4
	}

	for i := 0; i < answers; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+10 {
			return nil, 0, errors.New("short DNS answer")
		}
		answerType := binary.BigEndian.Uint16(msg[offset : offset+2])
		answerClass := binary.BigEndian.Uint16(msg[offset+2 : offset+4])
		answerTTL := binary.BigEndian.Uint32(msg[offset+4 : offset+8])
		rdLen := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if len(msg) < offset+rdLen {
			return nil, 0, errors.New("short DNS answer data")
		}
		if answerType == 1 && answerClass == 1 && rdLen == net.IPv4len {
			ip := net.IPv4(msg[offset], msg[offset+1], msg[offset+2], msg[offset+3])
			ttl := maxTTL
			if answerTTL > 0 {
				answerDuration := time.Duration(answerTTL) * time.Second
				if ttl <= 0 || answerDuration < ttl {
					ttl = answerDuration
				}
			}
			if ttl <= 0 {
				ttl = time.Minute
			}
			return ip, ttl, nil
		}
		offset += rdLen
	}

	return nil, 0, fmt.Errorf("no A record for %s", host)
}

func skipDNSName(msg []byte, offset int) (int, error) {
	for {
		if offset >= len(msg) {
			return 0, errors.New("short DNS name")
		}
		l := int(msg[offset])
		switch l & 0xc0 {
		case 0x00:
			offset++
			if l == 0 {
				return offset, nil
			}
			if offset+l > len(msg) {
				return 0, errors.New("short DNS label")
			}
			offset += l
		case 0xc0:
			if offset+2 > len(msg) {
				return 0, errors.New("short DNS compression pointer")
			}
			return offset + 2, nil
		default:
			return 0, errors.New("unsupported DNS name label")
		}
	}
}
