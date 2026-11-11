package core

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubaccount"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// An approved hub takes an instruction only with a valid code for itself, no older
// than the last one it took of that kind (review R-07); it lists what it ignored.
func TestSignedInstructions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dk, _ := hubcrypto.NewKey()
	if err := st.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	st.SetMeta(MetaHubID, "hub-a")
	h := New(config.Default(), "test", st)
	key, other := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	const kind = "facility-hub-admin/central-move"
	sign := func(keys map[string][]byte, obj, issued string) json.RawMessage {
		b, err := hubaccount.Sign(keys, kind, json.RawMessage(obj), issued)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	move := `{"url":"https://new.example.org"}`
	valid := sign(map[string][]byte{"hub-a": key, "hub-b": other}, move, "2026-11-11T10:00:00.000Z")

	if err := h.SaveRuntime(&Runtime{}); err != nil {
		t.Fatal(err)
	}
	if h.Signed(kind, valid) || len(h.Unsigned()) != 0 {
		t.Fatal("a hub without a move key took a signed instruction, or listed it")
	}
	h.UpdateRuntime(func(rt *Runtime) { rt.MoveKey = key })

	forged := bytes.Replace(valid, []byte("new.example.org"), []byte("evil.example.org"), 1)
	for name, obj := range map[string]json.RawMessage{
		"unsigned":                          json.RawMessage(move),
		"forged":                            forged,
		"for other hubs":                    sign(map[string][]byte{"hub-b": other}, move, "2026-11-11T10:00:00.000Z"),
		"with a code made with another key": sign(map[string][]byte{"hub-a": other}, move, "2026-11-11T10:00:00.000Z"),
		"of another kind": func() json.RawMessage {
			b, _ := hubaccount.Sign(map[string][]byte{"hub-a": key}, "facility-hub-admin/release", json.RawMessage(move), "2026-11-11T10:00:00.000Z")
			return b
		}(),
		"without issued": json.RawMessage(bytes.Replace(valid, []byte(`"issued":"2026-11-11T10:00:00.000Z"`), []byte(`"issued":""`), 1)),
	} {
		if h.Signed(kind, obj) {
			t.Errorf("%s: taken", name)
		}
		if !slices.Equal(h.Unsigned(), []string{kind}) {
			t.Errorf("%s: listed %v", name, h.Unsigned())
		}
	}
	if !h.Signed(kind, valid) || len(h.Unsigned()) != 0 {
		t.Fatalf("a valid instruction was not taken (listed %v)", h.Unsigned())
	}
	if !h.Signed(kind, valid) {
		t.Error("the same instruction read again was not taken")
	}
	newer := sign(map[string][]byte{"hub-a": key}, `{"url":""}`, "2026-11-11T11:00:00.000Z")
	if !h.Signed(kind, newer) {
		t.Error("a newer instruction was not taken")
	}
	if h.Signed(kind, valid) {
		t.Error("an older instruction, replayed, was taken")
	}
}
