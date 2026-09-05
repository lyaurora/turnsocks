package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func readServiceInfo() serviceInfo {
	var info serviceInfo
	output := commandOutput(2*time.Second, "systemctl", "show", "--property=ActiveState,MainPID", serviceName)
	for _, line := range strings.Split(output, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			info.Active = value == "active"
		case "MainPID":
			if value != "0" {
				info.PID = value
			}
		}
	}
	return info
}

func restartTurnsocks(listen string) error {
	if err := runCommand(10*time.Second, "sudo", "-n", "systemctl", "restart", serviceName); err != nil {
		return fmt.Errorf("重启 %s 失败：%w", serviceName, err)
	}
	return waitTurnsocksReady(8*time.Second, listen)
}

func waitTurnsocksReady(timeout time.Duration, listen string) error {
	deadline := time.Now().Add(timeout)
	checkAddr := localCheckAddr(listen)
	lastErr := errors.New("启动检查超时")
	for time.Now().Before(deadline) {
		info := readServiceInfo()
		pid, err := strconv.Atoi(info.PID)
		if !info.Active || err != nil || pid <= 0 {
			return fmt.Errorf("%s 未处于运行状态", serviceName)
		}
		lastErr = checkProxyReady(checkAddr, pid, min(500*time.Millisecond, time.Until(deadline)))
		if lastErr == nil {
			confirmed := readServiceInfo()
			if confirmed.Active && confirmed.PID == info.PID {
				return nil
			}
			return fmt.Errorf("%s 在启动检查期间退出或重启", serviceName)
		}
		time.Sleep(min(250*time.Millisecond, time.Until(deadline)))
	}
	return fmt.Errorf("%s 未在 %s 恢复监听：%w", serviceName, checkAddr, lastErr)
}

func checkProxyReady(addr string, pid int, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 0} {
		return errors.New("SOCKS5 握手应答无效")
	}

	// Match the accepted connection, not just a listening port: another SOCKS
	// proxy may own that port while this service is still starting.
	client := conn.LocalAddr().(*net.TCPAddr)
	server := conn.RemoteAddr().(*net.TCPAddr)
	proc := fmt.Sprintf("/proc/%d", pid)
	inode := ""
	for _, table := range []string{"tcp", "tcp6"} {
		local, remote := procTCPAddr(server, table == "tcp6"), procTCPAddr(client, table == "tcp6")
		if local == "" || remote == "" {
			continue
		}
		raw, err := os.ReadFile(proc + "/net/" + table)
		if errors.Is(err, os.ErrNotExist) && table == "tcp6" {
			continue
		}
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 10 && fields[1] == local && fields[2] == remote && fields[3] == "01" {
				inode = "socket:[" + fields[9] + "]"
				break
			}
		}
		if inode != "" {
			break
		}
	}
	if inode == "" {
		return errors.New("无法核验 SOCKS5 应答连接")
	}
	// ponytail: scan descriptors only on restarts; use socket diagnostics if large fd tables become slow.
	fds, err := os.ReadDir(proc + "/fd")
	if err != nil {
		return err
	}
	for _, fd := range fds {
		if target, err := os.Readlink(proc + "/fd/" + fd.Name()); err == nil && target == inode {
			return nil
		}
	}
	return fmt.Errorf("SOCKS5 应答连接不属于 %s 进程 %d", serviceName, pid)
}

func procTCPAddr(addr *net.TCPAddr, ipv6 bool) string {
	ip := addr.IP.To4()
	if ipv6 {
		ip = addr.IP.To16()
	}
	if ip == nil {
		return ""
	}
	var encoded strings.Builder
	for i := 0; i < len(ip); i += 4 {
		fmt.Fprintf(&encoded, "%08X", binary.NativeEndian.Uint32(ip[i:i+4]))
	}
	fmt.Fprintf(&encoded, ":%04X", addr.Port)
	return encoded.String()
}

func localCheckAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		if ip != nil && ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, port)
}

func runCommand(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Run()
}

func commandOutput(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeAPIError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, apiResponse{OK: false, Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
