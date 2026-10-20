package devkey

import (
	"errors"
	"io/fs"

	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// Open opens this machine's TPM through the kernel's resource manager, or returns nil
// when it has none.
func Open() (Device, error) {
	t, err := linuxtpm.Open("/dev/tpmrm0")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return NewTPM(t), nil
}
