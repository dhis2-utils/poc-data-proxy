// Command hubtest prepares DHIS2 instances for the hub's live tests (docs/TESTING.md).
//
//	hubtest provision -profile synthetic -url http://dhis2-x:8080 -admin local_admin:district -out manifest.json
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/testkit/provision"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hubtest provision -profile synthetic -url URL [-admin user:pass] [-size n] [-out manifest.json]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "provision":
		fs := flag.NewFlagSet("provision", flag.ExitOnError)
		profile := fs.String("profile", "synthetic", "synthetic, sierra-leone or laos")
		url := fs.String("url", "", "DHIS2 base URL")
		admin := fs.String("admin", "local_admin:district", "superuser user:password")
		size := fs.Int("size", 1, "population scale (synthetic)")
		out := fs.String("out", "manifest.json", "manifest to write")
		fs.Parse(os.Args[2:])
		user, pass, _ := strings.Cut(*admin, ":")
		c := testkit.NewCentral(*url, user, pass)
		var m *testkit.Manifest
		var err error
		switch *profile {
		case "synthetic":
			m, err = provision.Synthetic(c, *size)
		default:
			err = fmt.Errorf("unknown profile %q", *profile)
		}
		if err != nil {
			log.Fatal(err)
		}
		if err := m.Save(*out); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s: %d roles, %d matrix users, %d programs", *out, len(m.Users), len(m.Matrix), len(m.Programs))
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}
