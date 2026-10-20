package devkey

import "github.com/dhis2/facility-hub/internal/hubcrypto"

// Fake is a Device for tests: a key of its own stands in for a TPM's, so a blob sealed
// by one Fake does not open with another (a disk moved to another machine).
type Fake struct{ k hubcrypto.Key }

func NewFake() *Fake {
	k, err := hubcrypto.NewKey()
	if err != nil {
		panic(err)
	}
	return &Fake{k}
}

func (f *Fake) Seal(s []byte) ([]byte, error)   { return hubcrypto.Seal(f.k, s, []byte("fake tpm")) }
func (f *Fake) Unseal(b []byte) ([]byte, error) { return hubcrypto.Open(f.k, b, []byte("fake tpm")) }
func (f *Fake) Close() error                    { return nil }
