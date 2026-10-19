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
