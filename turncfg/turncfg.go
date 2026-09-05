package turncfg

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"unicode"
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
	if strings.Contains(raw, ",") || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return Server{}, errors.New("节点不能包含逗号、换行或控制字符")
	}

	server := Server{Raw: raw}
	addr := raw
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		cred := raw[:at]
		addr = raw[at+1:]
		user, pass, ok := strings.Cut(cred, ":")
		if !ok || user == "" || pass == "" {
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
	if strings.ContainsAny(host, "/\\?#\"'") || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return Server{}, errors.New("节点主机名不能包含空白或 URL 特殊字符")
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

// DecodeEnvValue accepts plain values and paired shell-style quotes.
func DecodeEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != value[len(value)-1] {
		return value
	}
	switch value[0] {
	case '\'':
		return value[1 : len(value)-1]
	case '"':
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\$`, `$`, "\\`", "`").Replace(value[1 : len(value)-1])
	default:
		return value
	}
}

// EncodeEnvValue preserves single-line values when read by Go or systemd.
func EncodeEnvValue(value string) string {
	if value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\"'\\") {
		return value
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
