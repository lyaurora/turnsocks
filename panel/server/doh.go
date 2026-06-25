package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func checkDoHEndpoint(endpoint string) error {
	query, queryID, err := buildDNSAQuery("cloudflare.com")
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
	if _, err := parseDNSAResponse(raw, queryID); err != nil {
		return err
	}
	return nil
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
	var out []byte
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" {
			return nil, fmt.Errorf("invalid DNS host %q", host)
		}
		if len(label) > 63 {
			return nil, fmt.Errorf("DNS label too long in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	return out, nil
}

func parseDNSAResponse(msg []byte, wantID uint16) (net.IP, error) {
	if len(msg) < 12 {
		return nil, errors.New("short DNS response")
	}
	if gotID := binary.BigEndian.Uint16(msg[0:2]); gotID != wantID {
		return nil, errors.New("DNS response ID mismatch")
	}
	if msg[3]&0x0f != 0 {
		return nil, fmt.Errorf("DNS response code %d", msg[3]&0x0f)
	}
	questions := int(binary.BigEndian.Uint16(msg[4:6]))
	answers := int(binary.BigEndian.Uint16(msg[6:8]))
	offset := 12
	var err error
	for i := 0; i < questions; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, err
		}
		if len(msg) < offset+4 {
			return nil, errors.New("short DNS question")
		}
		offset += 4
	}
	for i := 0; i < answers; i++ {
		offset, err = skipDNSName(msg, offset)
		if err != nil {
			return nil, err
		}
		if len(msg) < offset+10 {
			return nil, errors.New("short DNS answer")
		}
		answerType := binary.BigEndian.Uint16(msg[offset : offset+2])
		answerClass := binary.BigEndian.Uint16(msg[offset+2 : offset+4])
		rdLen := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if len(msg) < offset+rdLen {
			return nil, errors.New("short DNS answer data")
		}
		if answerType == 1 && answerClass == 1 && rdLen == net.IPv4len {
			return net.IPv4(msg[offset], msg[offset+1], msg[offset+2], msg[offset+3]), nil
		}
		offset += rdLen
	}
	return nil, errors.New("no A record")
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
