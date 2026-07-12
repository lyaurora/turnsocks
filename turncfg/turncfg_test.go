package turncfg

import "testing"

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
