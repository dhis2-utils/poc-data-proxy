package hubaccount

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"
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

// address is the form of an address a move code is made for, the same pattern as the
// Facility Hub app's (codes.ts): http(s)://, a host name or an [IPv6] address, an
// optional numeric port, a path of letters, digits and - . _ ~ ! $ & ' ( ) * + , ; = : @ /.
// Anything else has more than one written form, and the hub and the app must agree.
var address = regexp.MustCompile(`^([Hh][Tt][Tt][Pp][Ss]?)://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?(/[A-Za-z0-9\-._~!$&'()*+,;=:@/]*)?$`)

// trimAddress removes surrounding ASCII white space only (the app does the same).
func trimAddress(raw string) string { return strings.Trim(raw, " \t\r\n") }

// CheckAddress refuses an address a move code is never made for (see address).
func CheckAddress(raw string) error {
	if !address.MatchString(trimAddress(raw)) {
		return fmt.Errorf("an address like https://central.example.org/dhis, of letters, digits and - . _ ~ ! $ & ' ( ) * + , ; = / : @ only")
	}
	return nil
}

// NormalizeAddress is the form of an address a move code is made for (it must pass
// CheckAddress): no surrounding spaces or trailing slash, scheme and host in lower
// case, no default port (443 for https, 80 for http), the path as typed.
func NormalizeAddress(raw string) string {
	s := strings.TrimRight(trimAddress(raw), "/")
	m := address.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	scheme, host, port := strings.ToLower(m[1]), strings.ToLower(m[2]), m[3]
	if (scheme == "https" && port == ":443") || (scheme == "http" && port == ":80") {
		port = ""
	}
	return scheme + "://" + host + port + m[4]
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
