// Command hubtest prepares DHIS2 instances for the hub's live tests and runs them
// (docs/TESTING.md).
//
//	hubtest provision -profile synthetic -url http://dhis2-x:8080 -out manifest.json
//	hubtest run -profile sierra-leone -version 42          fresh instance, provision, live suite
//	hubtest matrix -profiles synthetic,sierra-leone,laos -versions 42
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/testkit/provision"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hubtest provision|run|matrix [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "provision":
		err = cmdProvision(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "matrix":
		err = cmdMatrix(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

type provisionFlags struct {
	profile, facility string
	size, programs    int
	keepPublic        bool
}

func (p *provisionFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&p.profile, "profile", "synthetic", "synthetic, sierra-leone or laos")
	fs.IntVar(&p.size, "size", 1, "population scale (synthetic)")
	fs.StringVar(&p.facility, "facility", "", "facility UID (seeded profiles; default: the one with most tracker data)")
	fs.IntVar(&p.programs, "programs", 4, "at most this many tracker programs (seeded profiles)")
	fs.BoolVar(&p.keepPublic, "keep-public", false, "keep the database's public sharing (seeded profiles)")
}

func (p *provisionFlags) provision(c *testkit.Central) (*testkit.Manifest, error) {
	seeded := func(prefix string) (*testkit.Manifest, error) {
		return provision.Seeded(c, provision.SeededOptions{Name: p.profile, Prefix: prefix, Facility: p.facility,
			Programs: p.programs, KeepPublic: p.keepPublic})
	}
	switch p.profile {
	case "synthetic":
		return provision.Synthetic(c, p.size)
	case "sierra-leone":
		return seeded("Hsl")
	case "laos":
		return seeded("Hla")
	}
	return nil, fmt.Errorf("unknown profile %q", p.profile)
}

// seeds maps a profile and DHIS2 major to a broker seed ("" = empty instance).
func seed(profile, version string) (string, error) {
	switch profile {
	case "synthetic":
		return "", nil
	case "sierra-leone":
		return "dhis2-db-sierra-leone_v" + version + ".sql.gz", nil
	case "laos":
		return "lao_hmis_demo_v" + version + ".sql.gz", nil
	}
	return "", fmt.Errorf("unknown profile %q", profile)
}

func cmdProvision(args []string) error {
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	var p provisionFlags
	p.register(fs)
	url := fs.String("url", "", "DHIS2 base URL")
	admin := fs.String("admin", "local_admin:district", "superuser user:password")
	out := fs.String("out", "manifest.json", "manifest to write")
	fs.Parse(args)
	user, pass, _ := strings.Cut(*admin, ":")
	m, err := p.provision(testkit.NewCentral(*url, user, pass))
	if err != nil {
		return err
	}
	if err := m.Save(*out); err != nil {
		return err
	}
	log.Printf("wrote %s: %d roles, %d matrix users, %d programs", *out, len(m.Users), len(m.Matrix), len(m.Programs))
	return nil
}

type runFlags struct {
	provisionFlags
	version, name, out, tests string
	reuse, stop               bool
}

func (r *runFlags) register(fs *flag.FlagSet) {
	r.provisionFlags.register(fs)
	fs.StringVar(&r.version, "version", "42", "DHIS2 major version")
	fs.StringVar(&r.name, "name", "", "broker instance (default agent-hubtest-<profile>-<version>)")
	fs.StringVar(&r.out, "out", "testresults", "results folder")
	fs.StringVar(&r.tests, "run", "", "go test -run pattern for the live suite")
	fs.BoolVar(&r.reuse, "reuse", false, "use the existing manifest and instance as they are")
	fs.BoolVar(&r.stop, "stop", true, "stop the instance afterwards (it keeps its state)")
}

// run gets a fresh instance, provisions it, and runs the live suite against it.
func (r *runFlags) run() error {
	name := r.name
	if name == "" {
		name = "agent-hubtest-" + r.profile + "-" + r.version
	}
	dir, _ := filepath.Abs(filepath.Join(r.out, r.profile+"-"+r.version))
	os.MkdirAll(dir, 0o755)
	manifest := filepath.Join(dir, "manifest.json")
	b, err := newBroker()
	if err != nil {
		return err
	}
	if r.reuse {
		if err := b.running(name); err != nil {
			return err
		}
	} else {
		s, err := seed(r.profile, r.version)
		if err != nil {
			return err
		}
		url, err := b.fresh(name, r.version, s, "facility hub live tests: "+r.profile+" "+r.version)
		if err != nil {
			return err
		}
		m, err := r.provision(testkit.NewCentral(url, "local_admin", "district"))
		if err != nil {
			return err
		}
		if err := m.Save(manifest); err != nil {
			return err
		}
	}
	log.Printf("%s %s: running the live suite", r.profile, r.version)
	args := []string{"test", "./internal/live/", "-count=1", "-v", "-timeout=2h"}
	if r.tests != "" {
		args = append(args, "-run", r.tests)
	}
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "HUB_MANIFEST="+manifest, "HUB_REPORT="+filepath.Join(dir, "oracle.json"))
	logf, _ := os.Create(filepath.Join(dir, "test.log"))
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	testErr := cmd.Run()
	if r.stop {
		if err := b.stop(name); err != nil {
			log.Printf("stopping %s: %v", name, err)
		}
	}
	if testErr != nil {
		return fmt.Errorf("%s %s: live suite failed (%v); see %s", r.profile, r.version, testErr, filepath.Join(dir, "test.log"))
	}
	log.Printf("%s %s: passed; log in %s", r.profile, r.version, filepath.Join(dir, "test.log"))
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var r runFlags
	r.register(fs)
	fs.Parse(args)
	return r.run()
}

// cmdMatrix runs profiles x versions one at a time (two DHIS2 instances booting
// together starve each other).
func cmdMatrix(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	var r runFlags
	r.register(fs)
	profiles := fs.String("profiles", "synthetic,sierra-leone,laos", "profiles")
	versions := fs.String("versions", "42", "DHIS2 majors")
	fs.Parse(args)
	var failed []string
	for _, v := range strings.Split(*versions, ",") {
		for _, p := range strings.Split(*profiles, ",") {
			rr := r
			rr.profile, rr.version = p, v
			if err := rr.run(); err != nil {
				log.Print(err)
				failed = append(failed, p+"-"+v)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed: %s", strings.Join(failed, ", "))
	}
	return nil
}
