package dnswire

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAResponseTTL(test *testing.T) {
	for _, scenario := range []struct {
		name     string
		cnameTTL uint32
		aFirst   bool
		maxTTL   time.Duration
		want     time.Duration
	}{
		{"short alias", 1, false, 300 * time.Second, time.Second},
		{"A before alias", 1, true, 300 * time.Second, time.Second},
		{"zero alias TTL", 0, true, 300 * time.Second, 0},
		{"TTL cap", 300, false, time.Minute, time.Minute},
		{"uncapped", 600, false, 0, 300 * time.Second},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			msg, id, err := BuildAQuery("alias.example")
			if err != nil {
				test.Fatal(err)
			}
			binary.BigEndian.PutUint16(msg[2:4], 0x8180)
			binary.BigEndian.PutUint16(msg[6:8], 2)
			target, err := encodeName("target.example")
			if err != nil {
				test.Fatal(err)
			}
			cname := []byte{0xc0, 0x0c, 0, 5, 0, 1}
			cname = binary.BigEndian.AppendUint32(cname, scenario.cnameTTL)
			cname = binary.BigEndian.AppendUint16(cname, uint16(len(target)))
			cname = append(cname, target...)
			answer := append([]byte(nil), target...)
			answer = append(answer, 0, 1, 0, 1, 0, 0, 1, 44, 0, 4, 192, 0, 2, 1)
			if scenario.aFirst {
				msg = append(append(msg, answer...), cname...)
			} else {
				msg = append(append(msg, cname...), answer...)
			}
			ip, ttl, err := ParseAResponse(msg, id, "alias.example", scenario.maxTTL)
			if err != nil || !ip.Equal(net.IPv4(192, 0, 2, 1)) || ttl != scenario.want {
				test.Fatalf("IP = %v, TTL = %s, err = %v; want TTL %s", ip, ttl, err, scenario.want)
			}
		})
	}
}

func TestDNSWireValidation(test *testing.T) {
	maxHost := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)
	if _, _, err := BuildAQuery(maxHost); err != nil {
		test.Fatalf("maximum length DNS host rejected: %v", err)
	}
	for _, host := range []string{"", ".", "a..example", strings.Repeat("a", 64) + ".example", maxHost + "a"} {
		if _, _, err := BuildAQuery(host); err == nil {
			test.Fatalf("invalid DNS host accepted: %q", host)
		}
	}
	query, id, err := BuildAQuery(" example.com. ")
	if err != nil {
		test.Fatal(err)
	}
	if binary.BigEndian.Uint16(query[:2]) != id || string(query[2:]) != "\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01" {
		test.Fatalf("invalid A query: %x", query)
	}
	if _, _, err := ParseAResponse(query, id, "example.com", 0); err == nil {
		test.Fatal("response without an A record accepted")
	}
	response := append(query, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
	binary.BigEndian.PutUint16(response[2:4], 0x8180)
	binary.BigEndian.PutUint16(response[6:8], 1)
	if ip, ttl, err := ParseAResponse(response, id, "example.com", 0); err != nil || !ip.Equal(net.IPv4(192, 0, 2, 1)) || ttl != time.Minute {
		test.Fatalf("valid response rejected: %v, %s, %v", ip, ttl, err)
	}
	for length := range len(response) {
		if _, _, err := ParseAResponse(response[:length], id, "example.com", 0); err == nil {
			test.Fatalf("truncated response accepted at length %d", length)
		}
	}
	if _, _, err := ParseAResponse(response, id^1, "example.com", 0); err == nil {
		test.Fatal("response with mismatched ID accepted")
	}
	response[3] = 0x83
	if _, _, err := ParseAResponse(response, id, "example.com", 0); err == nil {
		test.Fatal("DNS error response accepted")
	}
}
