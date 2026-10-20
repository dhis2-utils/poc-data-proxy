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
