package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The plain-HTTP start page is off unless the image asks for it.
func TestHTTPListen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hub.yaml")
	os.WriteFile(p, []byte("data_dir: "+t.TempDir()+"\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != "" {
		t.Errorf("default %q", c.HTTPListen)
	}
	os.WriteFile(p, []byte("data_dir: "+t.TempDir()+"\nlisten: \":443\"\nhttp_listen: \":80\"\n"), 0o644)
	if c, err = Load(p); err != nil || c.HTTPListen != ":80" || c.Listen != ":443" {
		t.Fatalf("%v %q %q", err, c.HTTPListen, c.Listen)
	}
}

// The hub's own dhis2.local responder is for Windows; off unless asked for.
func TestMDNS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hub.yaml")
	os.WriteFile(p, []byte("data_dir: "+t.TempDir()+"\n"), 0o644)
	if c, _ := Load(p); c.MDNS {
		t.Fatal("on by default")
	}
	os.WriteFile(p, []byte("data_dir: "+t.TempDir()+"\nmdns: true\n"), 0o644)
	if c, err := Load(p); err != nil || !c.MDNS {
		t.Fatalf("%v %v", err, c.MDNS)
	}
}

// Review R-13: an unknown key (the specification's own release_channel, a key a newer
// release dropped, a typo) is reported for the log, not refused: a hub that exits on
// its config at every start is never rolled back.
func TestUnknownKeyIsReported(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hub.yaml")
	os.WriteFile(p, []byte("data_dir: /d\nrelease_channel: https://releases.example.org/hub/stable\nlisten: \":443\"\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":443" || c.DataDir != "/d" {
		t.Errorf("known keys not read: %q %q", c.Listen, c.DataDir)
	}
	if len(c.Unknown) != 1 || c.Unknown[0] != p+":2: release_channel" {
		t.Errorf("unknown %q", c.Unknown)
	}
	// A bad value of a known key is still refused.
	os.WriteFile(p, []byte("sync_interval: often\n"), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("bad duration accepted")
	}
}
