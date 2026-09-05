package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/lyaurora/turnsocks/dnswire"
)

func httpClientViaSOCKS(proxyAddr string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(&url.URL{Scheme: "socks5", Host: proxyAddr}),
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
		DisableCompression:    true,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func testSOCKSTCP(ctx context.Context, proxyAddr string) Check {
	start := time.Now()
	client := httpClientViaSOCKS(proxyAddr, 12*time.Second)
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://www.gstatic.com/generate_204", nil)
	if err != nil {
		return Check{Message: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Check{Message: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return Check{Message: fmt.Sprintf("连通性检查返回 HTTP %d，预期 204", resp.StatusCode)}
	}
	return Check{OK: true, Message: "TCP 转发可用", MS: elapsedMS(start)}
}

func testSOCKSUDP(ctx context.Context, proxyAddr string) Check {
	start := time.Now()
	tcpConn, udpAddr, err := socks5UDPAssociate(ctx, proxyAddr, 5*time.Second)
	if err != nil {
		return Check{Message: err.Error()}
	}
	defer tcpConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return Check{Message: err.Error()}
	}
	defer udpConn.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = tcpConn.Close()
		_ = udpConn.Close()
	})
	defer stop()
	_ = udpConn.SetDeadline(time.Now().Add(8 * time.Second))

	payload, txID, err := dnswire.BuildAQuery("cloudflare.com")
	if err != nil {
		return Check{Message: err.Error()}
	}
	packet := buildSOCKSUDPDatagram(net.ParseIP("1.1.1.1"), 53, payload)
	if _, err := udpConn.WriteToUDP(packet, udpAddr); err != nil {
		return Check{Message: err.Error()}
	}

	buf := make([]byte, 4096)
	n, _, err := udpConn.ReadFromUDP(buf)
	if err != nil {
		return Check{Message: err.Error()}
	}
	dnsPayload, err := parseSOCKSUDPDatagram(buf[:n])
	if err != nil {
		return Check{Message: err.Error()}
	}
	if len(dnsPayload) < 12 || binary.BigEndian.Uint16(dnsPayload[:2]) != txID || dnsPayload[2]&0x80 == 0 {
		return Check{Message: "DNS 响应不匹配"}
	}
	return Check{OK: true, Message: "UDP 转发可用", MS: elapsedMS(start)}
}

func socks5UDPAssociate(ctx context.Context, proxyAddr string, timeout time.Duration) (net.Conn, *net.UDPAddr, error) {
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	buf := make([]byte, 260)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		_ = conn.Close()
		return nil, nil, errors.New("SOCKS5 无需认证模式被拒绝")
	}
	if _, err := conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 || buf[2] != 0x00 {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("UDP ASSOCIATE failed: 0x%02x", buf[1])
	}
	host, err := readSOCKS5Addr(conn, buf[3])
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	port := int(binary.BigEndian.Uint16(buf[:2]))
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if len(addrs) == 0 || port == 0 {
		_ = conn.Close()
		return nil, nil, errors.New("SOCKS5 返回了无效的 UDP 地址")
	}
	udpAddr := &net.UDPAddr{IP: addrs[0].IP, Zone: addrs[0].Zone, Port: port}
	_ = conn.SetDeadline(time.Time{})
	return conn, udpAddr, nil
}

func readSOCKS5Addr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01, 0x04:
		size := net.IPv4len
		if atyp == 0x04 {
			size = net.IPv6len
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 0x03:
		ln := []byte{0}
		if _, err := io.ReadFull(conn, ln); err != nil {
			return "", err
		}
		buf := make([]byte, int(ln[0]))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 addr atyp 0x%02x", atyp)
	}
}

func buildSOCKSUDPDatagram(ip net.IP, port int, payload []byte) []byte {
	ip4 := ip.To4()
	packet := []byte{0, 0, 0, 1}
	packet = append(packet, ip4...)
	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	return append(packet, payload...)
}

func parseSOCKSUDPDatagram(packet []byte) ([]byte, error) {
	if len(packet) < 4 || packet[2] != 0 {
		return nil, errors.New("invalid UDP relay response")
	}
	offset := 4
	switch packet[3] {
	case 0x01:
		offset += 4
	case 0x03:
		if len(packet) <= offset {
			return nil, errors.New("invalid UDP relay domain response")
		}
		offset += 1 + int(packet[offset])
	case 0x04:
		offset += 16
	default:
		return nil, fmt.Errorf("unsupported UDP relay atyp 0x%02x", packet[3])
	}
	offset += 2
	if len(packet) < offset {
		return nil, errors.New("short UDP relay response")
	}
	return packet[offset:], nil
}
