package hubaccount

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
)

// Instructions the Facility Hub app writes in central's dataStore carry a code per
// approved hub, made with that hub's move key (docs/superpowers/specs/
// 2026-11-11-signed-instructions.md). The app computes the same (codes.ts);
// TestInstructionCodeVector holds a value both must give.

// InstructionCode is hub's code for obj, a JSON object, as an instruction of kind
// ("<namespace>/<key>" of its entry): base64 of the first 16 bytes of
// HMAC-SHA256(moveKey, "facility-hub instruction" 0 hubID 0 kind 0 canonical(obj
// without "codes")).
func InstructionCode(moveKey []byte, hubID, kind string, obj json.RawMessage) (string, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(obj))
	d.UseNumber() // numbers as written, not through float64
	if err := d.Decode(&m); err != nil {
		return "", err
	}
	if m == nil {
		return "", errors.New("not a JSON object")
	}
	delete(m, "codes")
	var b bytes.Buffer
	if err := canonical(&b, m); err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, moveKey)
	h.Write([]byte("facility-hub instruction\x00" + hubID + "\x00" + kind + "\x00"))
	h.Write(b.Bytes())
	return base64.StdEncoding.EncodeToString(h.Sum(nil)[:16]), nil
}

// canonical writes v with object keys sorted and no spaces. Strings are escaped as
// encoding/json does without HTML escaping, which is JSON.stringify's form but for
// U+2028 and U+2029 (the app escapes those too).
func canonical(b *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonical(b, k); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := canonical(b, v[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonical(b, x); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		e := json.NewEncoder(b)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // Encode's newline
	}
	return nil
}

// Sign returns obj with issued and a code for each hub in keys (hub ID: move key), as
// the Facility Hub app writes an instruction (signedEntry in its adminEntry.ts).
// Tools and tests use it; the hubs only check.
func Sign(keys map[string][]byte, kind string, obj json.RawMessage, issued string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	delete(m, "codes")
	m["issued"], _ = json.Marshal(issued)
	plain, _ := json.Marshal(m)
	codes := map[string]string{}
	for hub, key := range keys {
		c, err := InstructionCode(key, hub, kind, plain)
		if err != nil {
			return nil, err
		}
		codes[hub] = c
	}
	m["codes"], _ = json.Marshal(codes)
	return json.Marshal(m)
}
