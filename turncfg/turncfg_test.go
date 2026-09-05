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

func TestParseServerRejectsCredentialDelimiters(t *testing.T) {
	for _, raw := range []string{
		"user:pa,ss@turn.example:3478",
		"user:pa\nss@turn.example:3478",
	} {
		if _, err := ParseServer(raw); err == nil {
			t.Fatalf("ParseServer(%q) accepted an invalid credential", raw)
		}
	}
}
