package hubaccount

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
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
// screen. It covers the facility too, which the account is created for. 16
// characters (80 bits): the key in the entry is public, so a matching one must be out
// of reach of an offline search.
func AccountCode(pub []byte, hubID, facility string) string {
	sum := sha256.Sum256(append([]byte("facility-hub account code\x00"+hubID+"\x00"+facility+"\x00"), pub...))
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

// CheckAddress refuses an address a move code is never made for: anything but
// http(s)://host[:port][/path] written with letters, digits and - . _ ~ ! $ & ' ( ) * +
// , ; = / : @ [ ] only (no spaces, user, query, fragment or %-escapes), and a port, if
// any, given. The hub and the app must agree on an address's form, and other
// addresses have more than one.
func CheckAddress(raw string) error {
	raw = strings.TrimSpace(raw)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=/:@[]", c) >= 0) {
			return fmt.Errorf("an address of letters, digits and - . _ ~ ! $ & ' ( ) * + , ; = / : @ only")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if s := strings.ToLower(u.Scheme); (s != "https" && s != "http") || u.Host == "" || u.User != nil || u.Opaque != "" ||
		strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("an address like https://central.example.org/dhis")
	}
	return nil
}

// NormalizeAddress is the form of an address a move code is made for (it must pass
// CheckAddress): no surrounding spaces or trailing slash, scheme and host in lower
// case, no default port (443 for https, 80 for http), the path as typed.
func NormalizeAddress(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	host = strings.TrimSuffix(host, map[string]string{"https": ":443", "http": ":80"}[scheme])
	return scheme + "://" + host + u.EscapedPath()
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
