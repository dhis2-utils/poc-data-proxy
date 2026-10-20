package devkey

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// srkHandle is the persistent storage key Windows (and tpm2-tools' provisioning)
// creates. The hub seals under it when it is there: Windows keeps the owner password,
// so a new primary key under the owner would be refused. Otherwise, as on a fresh TPM
// board on a Pi, it seals under a primary key made from the owner seed each time.
const srkHandle = tpm2.TPMHandle(0x81000001)

// Blob kinds: which parent sealed it.
const (
	parentPersistent byte = 1
	parentPrimary    byte = 2
)

// TPM is a Device over a TPM 2.0 transport.
type TPM struct{ t transport.TPMCloser }

// NewTPM wraps an open TPM transport.
func NewTPM(t transport.TPMCloser) *TPM { return &TPM{t} }

func (d *TPM) Close() error { return d.t.Close() }

// parent returns the storage key to seal under, its kind, and a function to release it.
func (d *TPM) parent(kind byte) (tpm2.AuthHandle, byte, func(), error) {
	if kind != parentPrimary {
		if rp, err := (tpm2.ReadPublic{ObjectHandle: srkHandle}).Execute(d.t); err == nil {
			return tpm2.AuthHandle{Handle: srkHandle, Name: rp.Name, Auth: tpm2.PasswordAuth(nil)}, parentPersistent, func() {}, nil
		} else if kind == parentPersistent {
			return tpm2.AuthHandle{}, 0, nil, fmt.Errorf("the TPM's storage key is gone: %w", err)
		}
	}
	rsp, err := (tpm2.CreatePrimary{PrimaryHandle: tpm2.TPMRHOwner, InPublic: tpm2.New2B(tpm2.ECCSRKTemplate)}).Execute(d.t)
	if err != nil {
		return tpm2.AuthHandle{}, 0, nil, fmt.Errorf("TPM primary key: %w", err)
	}
	flush := func() { (tpm2.FlushContext{FlushHandle: rsp.ObjectHandle}).Execute(d.t) }
	return tpm2.AuthHandle{Handle: rsp.ObjectHandle, Name: rsp.Name, Auth: tpm2.PasswordAuth(nil)}, parentPrimary, flush, nil
}

// Seal seals secret to this TPM: no password, no PCR policy.
func (d *TPM) Seal(secret []byte) ([]byte, error) {
	parent, kind, release, err := d.parent(0)
	if err != nil {
		return nil, err
	}
	defer release()
	rsp, err := (tpm2.Create{
		ParentHandle: parent,
		InSensitive: tpm2.TPM2BSensitiveCreate{Sensitive: &tpm2.TPMSSensitiveCreate{
			Data: tpm2.NewTPMUSensitiveCreate(&tpm2.TPM2BSensitiveData{Buffer: secret}),
		}},
		InPublic: tpm2.New2B(tpm2.TPMTPublic{
			Type:    tpm2.TPMAlgKeyedHash,
			NameAlg: tpm2.TPMAlgSHA256,
			ObjectAttributes: tpm2.TPMAObject{FixedTPM: true, FixedParent: true, UserWithAuth: true,
				NoDA: true},
		}),
	}).Execute(d.t)
	if err != nil {
		return nil, fmt.Errorf("TPM seal: %w", err)
	}
	pub, priv := tpm2.Marshal(rsp.OutPublic), tpm2.Marshal(rsp.OutPrivate)
	out := []byte{kind}
	out = binary.BigEndian.AppendUint16(out, uint16(len(pub)))
	return append(append(out, pub...), priv...), nil
}

// Unseal opens a blob Seal made on this TPM.
func (d *TPM) Unseal(blob []byte) ([]byte, error) {
	if len(blob) < 3 || int(binary.BigEndian.Uint16(blob[1:3]))+3 > len(blob) {
		return nil, errors.New("not a sealed device key")
	}
	n := int(binary.BigEndian.Uint16(blob[1:3]))
	pub, err := tpm2.Unmarshal[tpm2.TPM2BPublic](blob[3 : 3+n])
	if err != nil {
		return nil, err
	}
	priv, err := tpm2.Unmarshal[tpm2.TPM2BPrivate](blob[3+n:])
	if err != nil {
		return nil, err
	}
	parent, _, release, err := d.parent(blob[0])
	if err != nil {
		return nil, err
	}
	defer release()
	lrsp, err := (tpm2.Load{ParentHandle: parent, InPrivate: *priv, InPublic: *pub}).Execute(d.t)
	if err != nil {
		return nil, fmt.Errorf("TPM load: %w", err)
	}
	defer (tpm2.FlushContext{FlushHandle: lrsp.ObjectHandle}).Execute(d.t)
	ursp, err := (tpm2.Unseal{ItemHandle: tpm2.AuthHandle{Handle: lrsp.ObjectHandle, Name: lrsp.Name,
		Auth: tpm2.PasswordAuth(nil)}}).Execute(d.t)
	if err != nil {
		return nil, fmt.Errorf("TPM unseal: %w", err)
	}
	return ursp.OutData.Buffer, nil
}
