// Command fakecentral serves the test stand-in for DHIS2 (internal/fakecentral) so the
// hub can be exercised end to end without a DHIS2 instance (scripts/test-upgrade.sh).
// POST /fake/down and /fake/up cut and restore the "uplink"; POST /fake/revoke?user=
// revokes a user's personal access tokens.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/dhis2/facility-hub/internal/fakecentral"
)

func main() {
	addr := flag.String("listen", ":8090", "listen address")
	flag.Parse()
	fc := fakecentral.New()
	fc.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001", map[string]string{"SeedEv00001": fakecentral.Stage1})
	mux := http.NewServeMux()
	mux.HandleFunc("/fake/down", func(w http.ResponseWriter, r *http.Request) { fc.SetDown(true) })
	mux.HandleFunc("/fake/up", func(w http.ResponseWriter, r *http.Request) { fc.SetDown(false) })
	mux.HandleFunc("/fake/revoke", func(w http.ResponseWriter, r *http.Request) { fc.RevokeTokensOf(r.URL.Query().Get("user")) })
	mux.Handle("/", fc)
	log.Printf("fake central on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
