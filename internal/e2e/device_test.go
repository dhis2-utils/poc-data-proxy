package e2e

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/devkey"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/store"
)

// slotOpensWithPasswordAlone reports whether user's key slot on the disk opens with the
// password alone: the offline guessing a disk taken without its TPM would allow.
func slotOpensWithPasswordAlone(e *env, user string) bool {
	u, err := e.app.Hub.Store.UserByName(user)
	if err != nil || u == nil {
		e.t.Fatalf("no user %s: %v", user, err)
	}
	_, err = hubcrypto.UnwrapDK(u.WrappedDK, fc.Password, u.KEKSalt)
	return err == nil
}

// A hub on a machine with a TPM seals its device key there (internal/devkey). The TLS
// keys are sealed with it, and staff key slots on the disk are bound to it, while the
// USB stick keeps slots with the password alone. A disk moved to another machine
// opens neither; the hub restores there from its stick.
func TestDeviceKeyProtectsTheDisk(t *testing.T) {
	usb := t.TempDir()
	e := &env{t: t, central: fc.New(), dir: t.TempDir(), device: devkey.NewFake()}
	e.central.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001", nil)
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.USBDir, c.USBRequireMount = usb, false }
	t.Cleanup(func() { devkey.Use(nil) })
	e.start()
	e.app.Stick.Tick(context.Background()) // at boot: the box's stick gets the hub's mark
	if devkey.Key() == nil {
		t.Fatal("no device key with a TPM")
	}
	if _, err := hubtls.Load(e.dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"tls.key", "ca.key"} {
		if !devkey.Sealed(filepath.Join(e.dir, f)) {
			t.Errorf("%s is not sealed with the device key", f)
		}
	}
	e.setup()
	a := e.web("nurse_a")
	a.login()
	if slotOpensWithPasswordAlone(e, "nurse_a") {
		t.Fatal("nurse_a's slot on the disk opens with the password alone")
	}
	id := e.app.Hub.HubID()
	e.app.Stick.Tick(context.Background())
	if _, err := store.UnwrapFromKeyFile(filepath.Join(usb, "facility-hub", id, "keys.db"), "nurse_a", fc.Password); err != nil {
		t.Fatalf("the stick's slot does not open with the password: %v", err)
	}
	e.offline()
	a.json("POST", postSync, newPatient("DevTe000001", "DevEn000001", "Device", "NAT-6161", nil), 200)
	e.app.Stick.Tick(context.Background())

	// The disk goes into another machine.
	e.stop()
	e.device = devkey.NewFake()
	e.start()
	if devkey.Problem() == "" {
		t.Fatal("no problem shown for a disk without its TPM")
	}
	if st, _ := e.web("nurse_a").do("POST", "/api/auth/login", map[string]string{"username": "nurse_a", "password": fc.Password}); st == 200 {
		t.Fatal("a staff login opened the disk on another machine")
	}
	// There, the hub is restored from its stick onto an empty disk.
	e.diskDies()
	if devkey.Problem() != "" {
		t.Fatalf("an empty disk on the new machine: %s", devkey.Problem())
	}
	e.restoreFromStick(id, "nurse_a", fc.Password)
	e.web("nurse_a").login()
	e.sync()
	if e.central.Object("DevTe000001") == nil {
		t.Fatal("the save on the stick did not reach central from the new machine")
	}
	if slotOpensWithPasswordAlone(e, "nurse_a") {
		t.Fatal("after the restore, nurse_a's slot is not bound to the new machine's TPM")
	}
}

// A hub that gets a TPM (or an upgrade that brings this) binds each slot at its
// user's next login, offline too; until then the old slot still opens.
func TestSlotsBoundAtNextLogin(t *testing.T) {
	e := newEnv(t)
	t.Cleanup(func() { devkey.Use(nil) })
	e.setup()
	e.web("nurse_a").login()
	if !slotOpensWithPasswordAlone(e, "nurse_a") {
		t.Fatal("without a TPM the slot needs only the password")
	}
	e.stop()
	e.device = devkey.NewFake()
	e.start()
	e.offline()
	e.web("nurse_a").login()
	if slotOpensWithPasswordAlone(e, "nurse_a") {
		t.Fatal("the slot was not bound at the next login")
	}
}

// I9c: after a restart the store is locked until a known user logs in. A new device's
// first request then gets a pass-through session; that session must not be cached, so
// once the store is unlocked the hub registers the user at the device's next request
// and the device works offline.
func TestI9cNewDeviceWhileStoreLocked(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	e.sync()
	// nurse_b has never logged in: the hub does not know them yet.
	if u, _ := e.app.Hub.Store.UserByName("nurse_b"); u != nil {
		t.Fatal("nurse_b already registered")
	}
	e.stop()
	e.start() // locked until a known user logs in
	dev := e.android("nurse_b")
	if st, b := dev.do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("the device's first request while the store is locked: %d %s", st, b)
	}
	if u, _ := e.app.Hub.Store.UserByName("nurse_b"); u != nil {
		t.Fatal("the hub registered a new user while the store was locked")
	}
	nurse2 := e.web("nurse_a")
	nurse2.login() // unlocks the store
	for range 3 {
		if st, b := dev.do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("the device after the unlock: %d %s", st, b)
		}
	}
	if u, _ := e.app.Hub.Store.UserByName("nurse_b"); u == nil {
		t.Fatal("I9c: the hub did not register the user once the store was unlocked")
	}
	e.sync()
	e.offline()
	if st, b := dev.do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Errorf("I9c: offline, the device whose first request came while the store was locked: %d %.200s", st, b)
	}
}
