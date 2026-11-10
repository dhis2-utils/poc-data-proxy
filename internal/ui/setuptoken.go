package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"github.com/dhis2/facility-hub/internal/devkey"
	"math/big"
	"os"
	"strings"

	"github.com/dhis2/facility-hub/internal/core"
)

// The setup token guards setup when the image does not fix the central server
// (REVIEW-2 F1): without it, whoever reaches a new hub first could point it, and
// every staff login it forwards, at their own server. The hub writes it at first
// boot; the depot prints it on the label.

// The setup code is 8 digits: it only proves that whoever sets the hub up holds the
// box (facility-hub/hub.txt on its USB stick). Wrong codes are throttled from every
// device together (auth: 50 per 15 minutes, then locked), so someone who stops at 49
// and waits out the window gets about 4,700 guesses a day: an even chance of finding
// one of 10^8 codes takes about 29 years, 1% about 7 months (review R-04, R-29). It is
// read from the stick, so its length costs nobody typing it from memory.
const tokenDigits = 8

func setupTokenPath(h *core.Hub) string { return h.SetupCodePath() }

// EnsureSetupToken creates the setup token if setup needs one and none exists.
func EnsureSetupToken(h *core.Hub) error {
	if h.Mode() != core.ModeSetup || !needToken(h) {
		return nil
	}
	if _, err := os.Stat(setupTokenPath(h)); err == nil {
		return nil
	}
	var sb strings.Builder
	for range tokenDigits {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return err
		}
		sb.WriteByte(byte('0' + n.Int64()))
	}
	return devkey.WriteFile(setupTokenPath(h), []byte(sb.String()+"\n"), 0o600)
}

// needToken: set-up needs the code from the box's USB stick, unless set-up only
// opens on the machine itself (Windows: being at the PC proves presence).
func needToken(h *core.Hub) bool {
	return h.SetupMode() == core.SetupToken && !h.Cfg.SetupLocalOnly
}

func checkSetupToken(h *core.Hub, given string) error {
	b, err := devkey.ReadFile(setupTokenPath(h))
	if err != nil {
		return errors.New("this hub has no setup token; restart it to create one")
	}
	// Spaces and dashes do not count, on either side: a code of the earlier form
	// (XXXX-XXXX-XXXX-XXXX) still works until it is used.
	norm := strings.NewReplacer(" ", "", "-", "")
	want := norm.Replace(strings.TrimSpace(string(b)))
	given = norm.Replace(strings.ToUpper(strings.TrimSpace(given)))
	if subtle.ConstantTimeCompare([]byte(given), []byte(want)) != 1 {
		return errors.New("wrong setup code: use the one in facility-hub/hub.txt on the hub's USB stick")
	}
	return nil
}
