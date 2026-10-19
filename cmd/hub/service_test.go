package main

import "testing"

func TestServiceArgs(t *testing.T) {
	for _, c := range []struct {
		args             []string
		action, cfg, exe string
		bad              bool
	}{
		{[]string{"run", "-config", `C:\ProgramData\DHIS2 Facility Hub\hub.yaml`}, "run", `C:\ProgramData\DHIS2 Facility Hub\hub.yaml`, "", false},
		{[]string{"install", "-config", "hub.yaml", "-exe", `C:\x\hub.exe`}, "install", "hub.yaml", `C:\x\hub.exe`, false},
		{[]string{"uninstall"}, "uninstall", "", "", false},
		{[]string{"install", "-config", "hub.yaml", "-exe", "hub.exe", "-nostart"}, "install", "hub.yaml", "hub.exe", false},
		{[]string{"install", "-config", "hub.yaml"}, "", "", "", true}, // no exe
		{[]string{"run"}, "", "", "", true}, // no config
		{[]string{"restart"}, "", "", "", true},
		{nil, "", "", "", true},
	} {
		a, err := parseServiceArgs(c.args)
		if (err != nil) != c.bad || (!c.bad && (a.action != c.action || a.config != c.cfg || a.exe != c.exe)) {
			t.Errorf("%q: %+v %v", c.args, a, err)
		}
	}
	if a, _ := parseServiceArgs([]string{"install", "-config", "c", "-exe", "e", "-nostart"}); !a.noStart {
		t.Error("-nostart not read")
	}
}
