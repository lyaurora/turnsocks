package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lyaurora/turnsocks/dnswire"
)

type dnsEntry struct {
	IP       net.IP
	Err      error
	ExpireAt time.Time
}

const dnsNegativeTTL = 15 * time.Second

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

func resolveDoH(ctx context.Context, host string, cfg Config) (net.IP, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
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
			return e.IP, e.Err
		}
		dnsCache.Delete(queryHost)
	}

	return resolveDoHOnce(ctx, queryHost, cfg)
}

func resolveDoHOnce(ctx context.Context, queryHost string, cfg Config) (net.IP, error) {
	dnsLookupMu.Lock()
	call := dnsLookups[queryHost]
	if call == nil {
		call = &dnsLookupCall{done: make(chan struct{})}
		dnsLookups[queryHost] = call
		// ponytail: shared lookups keep their own timeout; individual waiters can leave early.
		go func() {
			lookupCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
			defer cancel()
			ip, err := queryDoH(lookupCtx, queryHost, cfg)
			if err != nil {
				cfg.TurnPool.recordFailure("", "DNS 解析", err)
			}
			call.result = dnsLookupResult{IP: ip, Err: err}
			dnsLookupMu.Lock()
			delete(dnsLookups, queryHost)
			close(call.done)
			dnsLookupMu.Unlock()
		}()
	}
	dnsLookupMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		return call.result.IP, call.result.Err
	}
}

func queryDoH(ctx context.Context, queryHost string, cfg Config) (net.IP, error) {
	u, err := buildDoHURL(cfg.DoH)
	if err != nil {
		return nil, err
	}
	query, queryID, err := dnswire.BuildAQuery(queryHost)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(query))
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
	ip, ttl, err := dnswire.ParseAResponse(raw, queryID, queryHost, cfg.DNSTTL)
	if err != nil {
		dnsCache.Store(queryHost, dnsEntry{Err: err, ExpireAt: time.Now().Add(dnsNegativeTTL)})
		return nil, err
	}
	if ttl > 0 {
		dnsCache.Store(queryHost, dnsEntry{IP: ip, ExpireAt: time.Now().Add(ttl)})
	}
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
