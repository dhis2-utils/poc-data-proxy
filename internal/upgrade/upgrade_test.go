package upgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte(`{"version":"0.2.0"}`)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)))
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if !Verify(pubB64, msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(pubB64, []byte(`{"version":"9.9.9"}`), sig) || Verify(pubB64, msg, nil) || Verify("", msg, sig) {
		t.Fatal("bad signature accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.2.0", "0.1.9", 1}, {"0.2.10", "0.2.9", 1}, {"1.0", "1.0.0", 0}, {"v0.1.0", "0.2.0", -1}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %d", c.a, c.b, got)
		}
	}
}

func TestInStage(t *testing.T) {
	if InStage(Choice{}, "hub-x") {
		t.Fatal("no pilots and 0 % takes nobody")
	}
	c := Choice{PilotHubs: []string{"hub-a"}}
	if !InStage(c, "hub-a") || InStage(c, "hub-b") {
		t.Fatal("pilot hubs")
	}
	c = Choice{Percent: 50}
	in := 0
	for i := 0; i < 1000; i++ {
		if InStage(c, "hub-"+string(rune('a'+i%26))+string(rune('a'+i/26%26))) {
			in++
		}
	}
	if in < 300 || in > 700 {
		t.Fatalf("50%% stage picked %d of 1000", in)
	}
	if !InStage(Choice{Percent: 100}, "hub-x") {
		t.Fatal("100 % takes everyone")
	}
}

// testdata/stages.json holds the hub and the Facility Hub app to one rule
// (apps/facility-hub/src/hubs/stage.test.ts reads the same table).
func TestStageTable(t *testing.T) {
	b, err := os.ReadFile("testdata/stages.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		HubID     string   `json:"hubId"`
		PilotHubs []string `json:"pilotHubs"`
		Percent   int      `json:"percent"`
		In        bool     `json:"in"`
	}
	if err := json.Unmarshal(b, &rows); err != nil || len(rows) < 40 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	for _, r := range rows {
		if got := InStage(Choice{PilotHubs: r.PilotHubs, Percent: r.Percent}, r.HubID); got != r.In {
			t.Errorf("%s at %d%%: %v, table says %v", r.HubID, r.Percent, got, r.In)
		}
	}
}
