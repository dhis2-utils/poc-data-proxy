package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubtls"
)

func TestInitKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	free := func(int) bool { return true }
	r1, err := initData(dir, false, free)
	if err != nil {
		t.Fatal(err)
	}
	yaml1, _ := os.ReadFile(filepath.Join(dir, "hub.yaml"))
	fp := hubtls.Fingerprint(dir)
	if fp == "" || r1.CA != filepath.Join(dir, "ca.crt") {
		t.Fatalf("no authority: %+v", r1)
	}
	if _, err := initData(dir, false, func(int) bool { return false }); err != nil {
		t.Fatal(err)
	}
	yaml2, _ := os.ReadFile(filepath.Join(dir, "hub.yaml"))
	if string(yaml1) != string(yaml2) || hubtls.Fingerprint(dir) != fp {
		t.Fatal("a second init changed the hub's settings or authority")
	}
}

func TestInitWritesNothingCountrySpecific(t *testing.T) {
	dir := t.TempDir()
	r, err := initData(dir, false, func(p int) bool { return p != 443 })
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hub.yaml"))
	s := string(b)
	if strings.Contains(s, "central_url") || !strings.Contains(s, `listen: ":8443"`) || !strings.Contains(s, `http_listen: ":8080"`) {
		t.Fatalf("hub.yaml:\n%s", s)
	}
	c, err := config.Load(filepath.Join(dir, "hub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A USB stick copy is chosen among removable drives on Windows; a Mac (a test
	// hub for the DHIS2 team) keeps none, so the app does not flag a missing stick.
	wantUSB := ""
	if runtime.GOOS == "windows" {
		wantUSB = "auto"
	}
	if !c.MDNS || !c.SetupLocalOnly || c.USBDir != wantUSB || c.DataDir != dir || r.HTTPSPort != 8443 {
		t.Fatalf("config %+v, result %+v", c, r)
	}
	// install.ini: what the NSIS installer reads (ReadINIStr).
	ini, _ := os.ReadFile(filepath.Join(dir, "install.ini"))
	for _, want := range []string{"[hub]", "https_port=8443", "http_port=8080", "ca=" + filepath.Join(dir, "ca.crt"), "ca_name=DHIS2 facility hub ", "url=https://dhis2.local:8443"} {
		if !strings.Contains(string(ini), want) {
			t.Errorf("install.ini lacks %q:\n%s", want, ini)
		}
	}
}

// The data folder is quoted in hub.yaml: a folder name with " #" in it is not cut off
// as a comment.
func TestInitQuotesPaths(t *testing.T) {
	// " #" starts a YAML comment, ": " a mapping. Windows allows no ":" in a folder
	// name, but every Windows path has one (C:\...).
	name := "Clinic #2: hub"
	if runtime.GOOS == "windows" {
		name = "Clinic #2 hub"
	}
	dir := filepath.Join(t.TempDir(), name)
	if _, err := initData(dir, false, func(int) bool { return true }); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(filepath.Join(dir, "hub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != dir {
		t.Fatalf("data_dir %q, want %q", c.DataDir, dir)
	}
	if runtime.GOOS == "windows" {
		return // Windows allows no " in a folder name
	}
	if _, err := initData(filepath.Join(t.TempDir(), `a"b`), false, func(int) bool { return true }); err == nil {
		t.Error(`a folder with " in its name accepted`)
	}
}
