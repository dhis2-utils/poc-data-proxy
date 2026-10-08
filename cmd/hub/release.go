package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhis2/facility-hub/internal/upgrade"
)

// keygen writes <out>.key (private, base64) and <out>.pub (public, base64; goes into
// the image defaults as release_public_key).
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "release", "output file prefix")
	fs.Parse(args)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out+".key", []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(*out+".pub", []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644)
}

// release copies binaries into a release channel folder and writes a signed manifest.
//
//	hub release -key release.key -dir channel -version 0.2.0 [-percent 100] [-hubs id,id] [-unsigned] linux/arm64=bin/hub-arm64 ...
func release(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	keyPath := fs.String("key", "release.key", "private key from keygen")
	dir := fs.String("dir", "channel", "release channel folder")
	ver := fs.String("version", "", "release version")
	percent := fs.Int("percent", 100, "staged rollout percentage")
	hubs := fs.String("hubs", "", "comma-separated hub IDs always in the stage")
	unsigned := fs.Bool("unsigned", false, "publish without a signature (to test that hubs ignore it)")
	centralURL := fs.String("central-url", "", "move every hub to this central URL (domain migration)")
	fs.Parse(args)
	if *ver == "" || fs.NArg() == 0 {
		return fmt.Errorf("need -version and at least one os/arch=binary")
	}
	m := upgrade.Manifest{Version: *ver, Files: map[string]string{}, SHA256: map[string]string{}}
	m.Stage.Percent = *percent
	m.CentralURL = *centralURL
	if *hubs != "" {
		m.Stage.HubIDs = strings.Split(*hubs, ",")
	}
	os.MkdirAll(*dir, 0o755)
	for _, a := range fs.Args() {
		plat, src, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("bad binary argument %q", a)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		name := "hub-" + *ver + "-" + strings.ReplaceAll(plat, "/", "-")
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m.Files[plat], m.SHA256[plat] = name, hex.EncodeToString(sum[:])
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(*dir, "release.json"), raw, 0o644); err != nil {
		return err
	}
	sigPath := filepath.Join(*dir, "release.json.sig")
	if *unsigned {
		os.Remove(sigPath)
		return nil
	}
	kb, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(kb)))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("bad private key")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(priv), raw)
	return os.WriteFile(sigPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}
