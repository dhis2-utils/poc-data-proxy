package devkey

import "github.com/google/go-tpm/tpm2/transport/windowstpm"

// Open opens this machine's TPM through Windows' TPM Base Services, or returns nil when
// it has none.
func Open() (Device, error) {
	t, err := windowstpm.Open()
	if err != nil {
		return nil, nil // no TPM, or TPM Base Services not available
	}
	return NewTPM(t), nil
}
