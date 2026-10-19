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
