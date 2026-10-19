package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"os"
	"strings"

	"github.com/dhis2/facility-hub/internal/core"
)

// The setup token guards setup when the image does not fix the central server
// (REVIEW-2 F1): without it, whoever reaches a new hub first could point it, and
// every staff login it forwards, at their own server. The hub writes it at first
// boot; the depot prints it on the label.

const tokenChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I

func setupTokenPath(h *core.Hub) string { return h.SetupCodePath() }

// EnsureSetupToken creates the setup token if setup needs one and none exists.
func EnsureSetupToken(h *core.Hub) error {
	if h.Mode() != core.ModeSetup || !needToken(h) {
		return nil
	}
	if _, err := os.Stat(setupTokenPath(h)); err == nil {
		return nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(tokenChars[int(c)%len(tokenChars)]) // 256 is a multiple of 32: no bias
	}
	return os.WriteFile(setupTokenPath(h), []byte(sb.String()+"\n"), 0o600)
}

// needToken: set-up needs the code from the box's USB stick, unless set-up only
// opens on the machine itself (Windows: being at the PC proves presence).
func needToken(h *core.Hub) bool {
	return h.SetupMode() == core.SetupToken && !h.Cfg.SetupLocalOnly
}

func checkSetupToken(h *core.Hub, given string) error {
	b, err := os.ReadFile(setupTokenPath(h))
	if err != nil {
		return errors.New("this hub has no setup token; restart it to create one")
	}
	want := strings.TrimSpace(string(b))
	given = strings.ToUpper(strings.TrimSpace(given))
	if subtle.ConstantTimeCompare([]byte(given), []byte(want)) != 1 {
		return errors.New("wrong setup token: use the one on the hub's label")
	}
	return nil
}
