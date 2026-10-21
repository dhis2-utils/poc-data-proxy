package main

import (
	"os"
	"testing"

	"github.com/dhis2/facility-hub/internal/devkey"
)

// The same files on every machine, with a TPM or not: internal/devkey tests the TPM.
func TestMain(m *testing.M) {
	devkey.OpenDevice = func() (devkey.Device, error) { return nil, nil }
	os.Exit(m.Run())
}

// The Windows installer asks its own hub.exe how it compares with the one installed
// ("hub version <installed>"): NSIS compares dotted numbers only, and would take the
// release 0.2.0 for older than the CI build 0.2.0-ci.57.
func TestVersionAgainst(t *testing.T) {
	for _, tc := range [][3]string{
		{"0.2.0", "0.2.0-ci.57", "newer"},
		{"0.2.0-ci.57", "0.2.0", "older"},
		{"0.2.0-ci.100", "0.2.0-ci.57", "newer"},
		{"0.2.0-ci.1", "0.1.4-test", "newer"},
		{"0.2.0", "0.2.0", "same"},
		{"0.1.3", "0.2.0-ci.1", "older"},
	} {
		if got := versionAgainst(tc[0], tc[1]); got != tc[2] {
			t.Errorf("%s against %s: %s, want %s", tc[0], tc[1], got, tc[2])
		}
	}
}
