package e2e

import (
	"net"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubtls"
)

// DEPLOYMENT.md, "Reaching the hub securely": the heartbeat carries the fingerprint of
// the authority devices install, for comparing in the Facility Hub app, and the hub's
// private addresses, the fallback where dhis2.local does not resolve.
func TestHeartbeatHasCertificateAndAddresses(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if _, err := hubtls.Load(e.dir, nil); err != nil {
		t.Fatal(err)
	}
	e.web("nurse_a").login()
	e.sync()
	hb := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())
	if hb["caFingerprint"] != hubtls.Fingerprint(e.dir) {
		t.Errorf("caFingerprint %v, want %s", hb["caFingerprint"], hubtls.Fingerprint(e.dir))
	}
	addrs, ok := hb["addresses"].([]any)
	if !ok {
		t.Fatalf("addresses %#v", hb["addresses"])
	}
	for _, a := range addrs {
		ip := net.ParseIP(a.(string))
		if ip == nil || ip.IsLoopback() || !(ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			t.Errorf("address %v", a)
		}
	}
}
