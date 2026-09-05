package dnswire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func BuildAQuery(host string) ([]byte, uint16, error) {
	id := uint16(time.Now().UnixNano())
	msg := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	name, err := encodeName(host)
	if err != nil {
		return nil, 0, err
	}
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return msg, id, nil
}

func encodeName(host string) ([]byte, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return nil, errors.New("empty DNS host")
	}
	var out []byte
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return nil, fmt.Errorf("invalid DNS host %q", host)
		}
		if len(label) > 63 {
			return nil, fmt.Errorf("DNS label too long in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	if len(out) > 254 {
		return nil, fmt.Errorf("DNS host too long %q", host)
	}
	out = append(out, 0)
	return out, nil
}

func ParseAResponse(msg []byte, wantID uint16, host string, maxTTL time.Duration) (net.IP, time.Duration, error) {
	if len(msg) < 12 {
		return nil, 0, errors.New("short DNS response")
	}
	if gotID := binary.BigEndian.Uint16(msg[0:2]); gotID != wantID {
		return nil, 0, errors.New("DNS response ID mismatch")
	}
	if msg[3]&0x0f != 0 {
		return nil, 0, fmt.Errorf("DNS response code %d", msg[3]&0x0f)
	}
	questions := int(binary.BigEndian.Uint16(msg[4:6]))
	answers := int(binary.BigEndian.Uint16(msg[6:8]))
	offset := 12
	var err error
	for range questions {
		offset, err = skipName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+4 {
			return nil, 0, errors.New("short DNS question")
		}
		offset += 4
	}

	var ip net.IP
	ttl := time.Duration(-1)
	for range answers {
		offset, err = skipName(msg, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(msg) < offset+10 {
			return nil, 0, errors.New("short DNS answer")
		}
		answerType := binary.BigEndian.Uint16(msg[offset : offset+2])
		answerClass := binary.BigEndian.Uint16(msg[offset+2 : offset+4])
		answerTTL := binary.BigEndian.Uint32(msg[offset+4 : offset+8])
		rdLen := int(binary.BigEndian.Uint16(msg[offset+8 : offset+10]))
		offset += 10
		if len(msg) < offset+rdLen {
			return nil, 0, errors.New("short DNS answer data")
		}
		if answerType == 1 && answerClass == 1 && rdLen == net.IPv4len {
			if ip == nil {
				ip = net.IPv4(msg[offset], msg[offset+1], msg[offset+2], msg[offset+3])
			}
		}
		// ponytail: the minimum answer TTL is conservative; track CNAME owners if it causes excess lookups.
		if answerClass == 1 && (answerType == 5 || answerType == 1 && rdLen == net.IPv4len) {
			recordTTL := time.Duration(answerTTL) * time.Second
			if ttl < 0 || recordTTL < ttl {
				ttl = recordTTL
			}
		}
		offset += rdLen
	}

	if ip == nil {
		return nil, 0, fmt.Errorf("no A record for %s", host)
	}
	if maxTTL > 0 && maxTTL < ttl {
		ttl = maxTTL
	}
	return ip, ttl, nil
}

func skipName(msg []byte, offset int) (int, error) {
	for {
		if offset >= len(msg) {
			return 0, errors.New("short DNS name")
		}
		labelLength := int(msg[offset])
		switch labelLength & 0xc0 {
		case 0x00:
			offset++
			if labelLength == 0 {
				return offset, nil
			}
			if offset+labelLength > len(msg) {
				return 0, errors.New("short DNS label")
			}
			offset += labelLength
		case 0xc0:
			if offset+2 > len(msg) {
				return 0, errors.New("short DNS compression pointer")
			}
			return offset + 2, nil
		default:
			return 0, errors.New("unsupported DNS name label")
		}
	}
}
