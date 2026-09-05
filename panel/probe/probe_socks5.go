package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

func testSOCKSUDP(proxyAddr string) Check {
	start := time.Now()
	tcpConn, udpAddr, err := socks5UDPAssociate(proxyAddr, 5*time.Second)
	if err != nil {
		return Check{Message: err.Error()}
	}
	defer tcpConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return Check{Message: err.Error()}
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(8 * time.Second))

	txID, payload := dnsQueryPayload("cloudflare.com")
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
	if len(dnsPayload) < 2 || binary.BigEndian.Uint16(dnsPayload[:2]) != txID {
		return Check{Message: "DNS 响应不匹配"}
	}
	return Check{OK: true, Message: "UDP 转发可用", MS: elapsedMS(start)}
}

func socks5UDPAssociate(proxyAddr string, timeout time.Duration) (net.Conn, *net.UDPAddr, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, nil, err
	}
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
	if buf[1] != 0x00 {
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
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, udpAddr, nil
}

func readSOCKS5Addr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
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

func dnsQueryPayload(name string) (uint16, []byte) {
	txID := uint16(time.Now().UnixNano())
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], txID)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	for _, part := range strings.Split(name, ".") {
		msg = append(msg, byte(len(part)))
		msg = append(msg, part...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return txID, msg
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
