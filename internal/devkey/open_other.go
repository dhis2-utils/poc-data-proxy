//go:build !linux && !windows

package devkey

// Open returns nil: the hub uses no TPM on this system (macOS is for the DHIS2 team's
// own tries, not facilities).
func Open() (Device, error) { return nil, nil }
