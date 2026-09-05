package probe

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("TURNSOCKS_PROBE_HELPER") == "1" {
		if err := runProbeHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The runner launches this test binary as its temporary SOCKS proxy. No public
// TURN server or download endpoint is contacted by these integration checks.
func runProbeHelper() error {
	args := flag.NewFlagSet("probe-helper", flag.ContinueOnError)
	listen := args.String("listen", "", "")
	for _, name := range []string{"config", "turns", "doh", "state", "timeout"} {
		args.String(name, "", "")
	}
	if err := args.Parse(os.Args[1:]); err != nil {
		return err
	}
	scenario := os.Getenv("TURNSOCKS_PROBE_SCENARIO")
	ready := func() { _ = os.WriteFile(os.Getenv("TURNSOCKS_PROBE_READY"), nil, 0600) }
	record := func(message string) {
		f, err := os.OpenFile(os.Getenv("TURNSOCKS_PROBE_REQUESTS"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			_, _ = fmt.Fprintln(f, message)
			_ = f.Close()
		}
	}
	if scenario == "startup" {
		ready()
		time.Sleep(time.Minute)
		return nil
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			var greeting [3]byte
			if _, err := io.ReadFull(conn, greeting[:]); err != nil {
				return // The startup readiness connection sends no greeting.
			}
			if scenario == "handshake" {
				ready()
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			_, _ = conn.Write([]byte{5, 0})
			var request [4]byte
			if _, err := io.ReadFull(conn, request[:]); err != nil {
				return
			}
			host, err := readSOCKS5Addr(conn, request[3])
			if err != nil {
				return
			}
			var port [2]byte
			if _, err := io.ReadFull(conn, port[:]); err != nil {
				return
			}
			reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
			if request[1] == 1 {
				_, _ = conn.Write(reply)
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_ = req.Body.Close()
				record(fmt.Sprintf("TCP %s:%d %s %s", host, binary.BigEndian.Uint16(port[:]), req.Method, req.RequestURI))
				status := http.StatusNoContent
				if scenario == "redirect" {
					status = http.StatusFound
				}
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nLocation: http://redirect.invalid/\r\n\r\n", status, http.StatusText(status))
				return
			}
			udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				return
			}
			defer udp.Close()
			binary.BigEndian.PutUint16(reply[8:], uint16(udp.LocalAddr().(*net.UDPAddr).Port))
			_, _ = conn.Write(reply)
			packet := make([]byte, 4096)
			n, peer, err := udp.ReadFromUDP(packet)
			if err != nil || n < 22 {
				return
			}
			record("UDP DNS")
			if scenario == "udp" {
				ready()
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			packet[12] |= 0x80 // DNS response bit, preserving the transaction ID.
			_, _ = udp.WriteToUDP(packet[:n], peer)
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
}

func TestLightCheckAndCancellation(t *testing.T) {
	for _, scenario := range []string{"success", "redirect", "startup", "handshake", "udp"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TURNSOCKS_BIN", bin)
			t.Setenv("TURNSOCKS_PROBE_HELPER", "1")
			t.Setenv("TURNSOCKS_PROBE_SCENARIO", scenario)
			t.Setenv("TURNSOCKS_PROBE_READY", filepath.Join(dir, "ready"))
			t.Setenv("TURNSOCKS_PROBE_REQUESTS", filepath.Join(dir, "requests"))
			t.Setenv("TMPDIR", dir)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_ = conn.Close()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			done := make(chan Result, 1)
			go func() {
				defer close(done)
				done <- (Runner{}).Test(ctx, Server{Raw: ln.Addr().String(), Addr: ln.Addr().String()}, "", ModeCheck)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("temporary proxy did not finish cleanup")
				}
			}()
			canceled := scenario != "success" && scenario != "redirect"
			if canceled {
				deadline := time.Now().Add(3 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("probe did not reach cancellation stage")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
			}
			select {
			case result := <-done:
				if canceled {
					if result.OK || result.Message != "检测已停止" {
						t.Fatalf("canceled result = %+v", result)
					}
				} else {
					if result.OK != (scenario == "success") || result.SOCKSTCP == nil || !result.SOCKSUDP.OK || result.SingleThread.Threads != 0 || result.MultiThread.Threads != 0 || result.DownloadBytes != 0 {
						t.Fatalf("light check result = %+v", result)
					}
					raw, err := os.ReadFile(filepath.Join(dir, "requests"))
					if err != nil || strings.TrimSpace(string(raw)) != "TCP www.gstatic.com:80 GET /generate_204\nUDP DNS" {
						t.Fatalf("light check must forward a small HTTP request and DNS only: %q, %v", raw, err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("probe did not finish promptly")
			}
			files, err := filepath.Glob(filepath.Join(dir, "turnsocks-panel-test-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary proxy files remain: %v, %v", files, err)
			}
		})
	}
}
