package hubaccount

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"net/url"
	"strings"
)

// Two codes people read out to each other (docs/FACILITY-HUB-APP.md). The Facility
// Hub app on central computes the same; TestCodes holds the values both must give.

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// AccountCode is what a hub waiting for an account shows on its admin page. The
// national team asks the facility for it before approving, and the app checks it
// against the key in the hub's registry entry: anyone in the hub owners group can
// write that entry, but no one can put a key there that matches the code on the hub's
// screen. 16 characters (80 bits): the key in the entry is public, so a matching one
// must be out of reach of an offline search.
func AccountCode(pub []byte, hubID string) string {
	sum := sha256.Sum256(append([]byte("facility-hub account code\x00"+hubID+"\x00"), pub...))
	return group(b32.EncodeToString(sum[:])[:16])
}

// MoveCode is what the national team gives a facility, after an unplanned move, with
// central's new address: the hub moves only to the address the code was made for,
// and checks it without sending anything. 10 characters (50 bits): it can only be
// guessed on the hub, which throttles wrong codes.
func MoveCode(moveKey []byte, address string) string {
	m := hmac.New(sha256.New, moveKey)
	m.Write([]byte(NormalizeAddress(address)))
	return group(b32.EncodeToString(m.Sum(nil))[:10])
}

// NormalizeAddress is the form of an address a move code is made for: no surrounding
// spaces or trailing slash, scheme and host in lower case, the path as typed.
func NormalizeAddress(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	return u.String()
}

// NormalizeCode is a code as typed, without spaces or dashes, in upper case.
func NormalizeCode(typed string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(typed))
}

// group writes a code in groups of five (move) or four (account) characters.
func group(s string) string {
	n := 4
	if len(s) == 10 {
		n = 5
	}
	var parts []string
	for len(s) > n {
		parts, s = append(parts, s[:n]), s[n:]
	}
	return strings.Join(append(parts, s), "-")
}
