package probe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPClientUsesSOCKSAndKeepAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := ln.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			var greeting [3]byte
			if _, err := io.ReadFull(conn, greeting[:]); err != nil {
				return err
			}
			if greeting != [3]byte{5, 1, 0} {
				return fmt.Errorf("invalid SOCKS greeting: %v", greeting)
			}
			if _, err := conn.Write([]byte{5, 0}); err != nil {
				return err
			}
			var request [4]byte
			if _, err := io.ReadFull(conn, request[:]); err != nil {
				return err
			}
			if request != [4]byte{5, 1, 0, 3} {
				return fmt.Errorf("SOCKS CONNECT did not preserve the domain: %v", request)
			}
			host, err := readSOCKS5Addr(conn, request[3])
			if err != nil || host != "proxy-test.invalid" {
				return fmt.Errorf("SOCKS target = %q: %v", host, err)
			}
			var port [2]byte
			if _, err := io.ReadFull(conn, port[:]); err != nil {
				return err
			}
			if port != [2]byte{0, 80} {
				return fmt.Errorf("SOCKS port = %v", port)
			}
			if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
				return err
			}
			reader := bufio.NewReader(conn)
			for range 2 {
				req, err := http.ReadRequest(reader)
				if err != nil {
					return err
				}
				_ = req.Body.Close()
				if req.Close || req.Host != "proxy-test.invalid" || req.RequestURI != "/payload" {
					return fmt.Errorf("unexpected tunneled request: %s, close=%v", req.RequestURI, req.Close)
				}
				if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
					return err
				}
			}
			return nil
		}()
	}()
	client := httpClientViaSOCKS(ln.Addr().String(), 2*time.Second)
	defer client.CloseIdleConnections()
	for range 2 {
		resp, err := client.Get("http://proxy-test.invalid/payload")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || string(body) != "ok" {
			t.Fatalf("download = %q, %v", body, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCancellationClosesSOCKSHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := downloadBytes(ctx, ln.Addr().String(), 1024)
		done <- err
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var greeting [3]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("download cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled download did not return")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := conn.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("cancelled SOCKS handshake was left open: %v", err)
	}
}
