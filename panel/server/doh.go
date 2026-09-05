package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lyaurora/turnsocks/dnswire"
)

func checkDoHEndpoint(endpoint string) error {
	query, queryID, err := dnswire.BuildAQuery("cloudflare.com")
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(query))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	_, _, err = dnswire.ParseAResponse(raw, queryID, "cloudflare.com", 0)
	return err
}
