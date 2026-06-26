package turncfg

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

type Server struct {
	Raw      string
	Addr     string
	Username string
	Password string
	HasAuth  bool
}

func ParseServer(raw string) (Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Server{}, errors.New("节点不能为空")
	}

	server := Server{Raw: raw}
	addr := raw
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		cred := raw[:at]
		addr = raw[at+1:]
		user, pass, ok := strings.Cut(cred, ":")
		if !ok || user == "" {
			return Server{}, errors.New("鉴权格式应为 user:pass@host:port")
		}
		server.Username = user
		server.Password = pass
		server.HasAuth = true
	}

	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return Server{}, errors.New("节点格式应为 host:port")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return Server{}, errors.New("端口必须是 1-65535")
	}

	server.Addr = addr
	if server.HasAuth {
		server.Raw = server.Username + ":" + server.Password + "@" + addr
	} else {
		server.Raw = addr
	}
	return server, nil
}
