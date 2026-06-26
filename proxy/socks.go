package proxy

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"time"
)

func handleSocksConn(conn net.Conn, cfg Config) {
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		return
	}

	if err := socksHandshake(conn); err != nil {
		if cfg.LogVerbose {
			log.Printf("SOCKS handshake failed: %v", err)
		}
		return
	}

	req, err := readSocksRequest(conn)
	if err != nil {
		if cfg.LogVerbose {
			log.Printf("SOCKS request failed: %v", err)
		}
		return
	}

	_ = conn.SetDeadline(time.Time{})

	switch req.Cmd {
	case 0x01:
		if req.Port == 0 {
			_ = writeSocksReply(conn, 0x01, "0.0.0.0", 0)
			return
		}
		handleTCPConnect(conn, cfg, req)
	case 0x03:
		handleUDPAssociate(conn, cfg)
	default:
		_ = writeSocksReply(conn, 0x07, "0.0.0.0", 0)
	}
}

type socksRequest struct {
	Cmd  byte
	Host string
	Port int
}

func socksHandshake(conn net.Conn) error {
	h := make([]byte, 2)
	if _, err := io.ReadFull(conn, h); err != nil {
		return err
	}
	if h[0] != 0x05 {
		return errors.New("not SOCKS5")
	}

	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	supportsNoAuth := false
	for _, method := range methods {
		if method == 0x00 {
			supportsNoAuth = true
			break
		}
	}
	if !supportsNoAuth {
		_ = writeAll(conn, []byte{0x05, 0xff})
		return errors.New("SOCKS client does not support no-auth method")
	}

	return writeAll(conn, []byte{0x05, 0x00})
}

func readSocksRequest(conn net.Conn) (socksRequest, error) {
	var r socksRequest

	h := make([]byte, 4)
	if _, err := io.ReadFull(conn, h); err != nil {
		return r, err
	}
	if h[0] != 0x05 {
		return r, errors.New("invalid SOCKS version")
	}
	if h[2] != 0x00 {
		return r, errors.New("invalid SOCKS reserved byte")
	}

	r.Cmd = h[1]
	host, port, err := readSocksAddr(conn, h[3])
	if err != nil {
		return r, err
	}
	r.Host = host
	r.Port = port
	return r, nil
}

func readSocksAddr(conn net.Conn, atyp byte) (string, int, error) {
	var host string

	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()

	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", 0, err
		}
		if l[0] == 0 {
			return "", 0, errors.New("empty domain name")
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = string(b)

	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()

	default:
		return "", 0, errors.New("unsupported ATYP")
	}

	p := make([]byte, 2)
	if _, err := io.ReadFull(conn, p); err != nil {
		return "", 0, err
	}
	port := int(binary.BigEndian.Uint16(p))
	return host, port, nil
}

func writeSocksReply(conn net.Conn, rep byte, bindHost string, bindPort int) error {
	ip := net.ParseIP(bindHost).To4()
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}
	b := []byte{0x05, rep, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], 0, 0}
	binary.BigEndian.PutUint16(b[8:10], uint16(bindPort))
	return writeAll(conn, b)
}

func writeAll(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		b = b[n:]
	}
	return nil
}

func handleTCPConnect(client net.Conn, cfg Config, req socksRequest) {
	ip, err := resolveDoH(req.Host, cfg)
	if err != nil {
		log.Printf("resolve failed %s: %v", req.Host, err)
		_ = writeSocksReply(client, 0x04, "0.0.0.0", 0)
		return
	}

	if cfg.LogVerbose {
		log.Printf("TCP CONNECT %s:%d -> %s:%d", req.Host, req.Port, ip.String(), req.Port)
	}

	dataConn, release, turnAddr, err := dialTurnTCP(cfg, ip, req.Port)
	if err != nil {
		log.Printf("TURN TCP failed %s:%d: %v", ip.String(), req.Port, err)
		_ = writeSocksReply(client, 0x05, "0.0.0.0", 0)
		return
	}
	if cfg.LogVerbose {
		log.Printf("TURN TCP selected %s for %s:%d", turnAddr, req.Host, req.Port)
	}
	defer release()
	defer dataConn.Close()

	if err := writeSocksReply(client, 0x00, "0.0.0.0", 0); err != nil {
		return
	}

	pipe(client, dataConn)
}

func pipe(a net.Conn, b net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		copyAndCloseWrite(a, b)
		done <- struct{}{}
	}()

	go func() {
		copyAndCloseWrite(b, a)
		done <- struct{}{}
	}()

	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}

func copyAndCloseWrite(dst net.Conn, src net.Conn) {
	bufp := proxyCopyBufferPool.Get().(*[]byte)
	_, err := io.CopyBuffer(dst, src, *bufp)
	proxyCopyBufferPool.Put(bufp)
	if err != nil {
		_ = dst.Close()
		_ = src.Close()
		return
	}
	closeWrite(dst)
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if c, ok := conn.(closeWriter); ok {
		_ = c.CloseWrite()
		return
	}
	_ = conn.Close()
}
