package testkit

import (
	"os/exec"
	"strings"
	"testing"
)

// The workflows run the repository's shell scripts directly (windows/check.sh,
// image/build.sh, …), so each must stay executable in git. An editor or `sed -i`
// that rewrites a file can drop the bit: it happened to image/build.sh and others
// (fixed in 98d4ecf), then to windows/check.sh (the review of pending-items,
// 23 November 2026).
func TestScriptsExecutable(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "-s", "--full-name", "--", ":/*.sh").Output()
	if err != nil {
		t.Skipf("no git checkout: %v", err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line) // mode, object, stage, path
		if len(f) < 4 {
			continue
		}
		n++
		if f[0] != "100755" {
			t.Errorf("%s is not executable in git (mode %s): git update-index --chmod=+x %s", f[3], f[0], f[3])
		}
	}
	if n == 0 {
		t.Fatal("found no shell scripts: the pattern is wrong")
	}
}
