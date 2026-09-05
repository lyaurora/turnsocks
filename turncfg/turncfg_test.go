package turncfg

import "testing"

func TestEnvValueQuotes(t *testing.T) {
	for _, value := range []string{"", "plain", "demo'", "'quoted'", "\"quoted\"", "pa\\ss\"'", " spaced ", "a\tb", "中文", "\\$\\\x60\\n\\t"} {
		if got := DecodeEnvValue(EncodeEnvValue(value)); got != value {
			t.Fatalf("round trip %q = %q", value, got)
		}
	}
	for encoded, want := range map[string]string{
		" 'plain' ":        "plain",
		"\"plain\"":        "plain",
		"'pa\\ss'":         "pa\\ss",
		"\"pa\\\\ss\\\"\"": "pa\\ss\"",
		"\"a\\nb\"":        "a\\nb",
		"\"\\$\\\x60\"":    "$\x60",
		"demo'":            "demo'",
		"\"demo":           "\"demo",
	} {
		if got := DecodeEnvValue(encoded); got != want {
			t.Fatalf("decode %q = %q, want %q", encoded, got, want)
		}
	}
}

func TestParseServerRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		"user:@turn.example:3478",
		":password@turn.example:3478",
		"user:pa,ss@turn.example:3478",
		"user:pa\nss@turn.example:3478",
		"user:pa\x00ss@turn.example:3478",
		"turn,example:3478",
		"turn.example\nPANEL_PASSWORD=demo:3478",
		"turn example:3478",
		"turn\u00a0example:3478",
		"turn.example/path:3478",
		"turn.example?query:3478",
		"turn.example#fragment:3478",
		"turn\\example:3478",
	} {
		if _, err := ParseServer(raw); err == nil {
			t.Errorf("ParseServer(%q) accepted an invalid node", raw)
		}
	}
	for _, raw := range []string{
		"localhost:3478",
		"turn.example:3478",
		"192.0.2.1:3478",
		"[2001:db8::1]:3478",
		"[fe80::1%eth0]:3478",
		"user:pa:ss@word@turn.example:3478",
		"user:pa ss'\"\\$@turn.example:3478",
	} {
		server, err := ParseServer(raw)
		if err != nil || server.Raw != raw {
			t.Errorf("valid node %q changed or rejected: %#v, %v", raw, server, err)
		}
	}
}
