package hubtls

import (
	"net"
	"testing"
	"time"
)

// The hub dates its own server certificate by its clock, an hour back (docs/design/2026-11-04-no-rtc.md).
// Devices check it by theirs. Issued while the hub's clock is ahead by more than an
// hour, the certificate is not yet valid for devices with the right time, and the hub
// keeps serving it after its clock is set right: it is renewed only near its end, or
// when the hub's names or addresses change.
func TestNoRTCCertificateIssuedAhead(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	right := f.now
	if _, err := load(dir, f.env()); err != nil { // set up with the right clock
		t.Fatal(err)
	}
	// Restarted with the clock two days ahead (a PC), on a new address: a new
	// certificate.
	f.now = right.Add(48 * time.Hour)
	f.addrs = []net.IP{net.ParseIP("192.168.1.21")}
	c, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "dhis2.local", right); err == nil {
		t.Error("a device with the right time takes a certificate dated two days ahead")
	}
	f.now = right // the clock set right: the next check (every 30 seconds) keeps it
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if again, _ := leafOf(t, c); !again.Equal(leaf) {
		t.Error("renewed once the clock was right; the test expected today's behaviour")
	}
	// Another address issues a new one, dated by the right clock.
	f.addrs = []net.IP{net.ParseIP("192.168.1.22")}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if leaf, pool = leafOf(t, c); verify(leaf, pool, "dhis2.local", right) != nil {
		t.Error("the certificate issued with the right clock does not verify")
	}
}

// An authority made at first boot with the clock ahead is not yet valid either: no
// new server certificate helps until the true time passes its start.
func TestNoRTCAuthorityMadeAhead(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	right := f.now
	f.now = right.Add(48 * time.Hour)
	if _, err := load(dir, f.env()); err != nil {
		t.Fatal(err)
	}
	f.now = right
	f.addrs = []net.IP{net.ParseIP("192.168.1.23")}
	c, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "dhis2.local", right); err == nil {
		t.Error("a chain from an authority dated two days ahead verifies")
	}
	if err := verify(leaf, pool, "dhis2.local", right.Add(48*time.Hour)); err != nil {
		t.Errorf("two days later: %v", err)
	}
}

// Issued while the hub's clock is weeks behind, the certificate verifies for devices
// with the right time: it starts earlier and ends earlier, and is renewed that much
// sooner. The authority made at first boot is dated the same way and lasts ten years.
func TestNoRTCCertificateIssuedBehind(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	right := f.now
	f.now = time.Date(2026, 10, 6, 0, 27, 36, 0, time.UTC) // the Pi 4 image's epoch
	f.addrs = []net.IP{net.ParseIP("192.168.1.30")}
	c, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "dhis2.local", right); err != nil {
		t.Errorf("issued at the epoch, checked on %s: %v", right.Format(time.DateOnly), err)
	}
	if got := right.Sub(f.now).Round(time.Hour); leaf.NotAfter.Sub(right.Add(leafLife)).Round(time.Hour) != -got {
		t.Errorf("ends %s, want %s earlier than one issued with the right clock", leaf.NotAfter, got)
	}
}
