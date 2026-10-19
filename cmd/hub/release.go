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
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhis2/facility-hub/internal/upgrade"
)

// keygen writes <out>.key (private, base64; the release workflow's signing secret) and
// <out>.pub (public, base64; built into the binary as upgrade.ReleaseKey).
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

// release writes a release into a folder, for uploading as the assets of the GitHub
// release v<version>: the binaries, the Pi image if given, and the signed manifest.
//
//	hub release -key release.key -dir out -version 0.3.0 [-image dhis2-hub-0.3.0-arm64.img.xz] linux/arm64=bin/hub-arm64 ...
func release(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	keyPath := fs.String("key", "release.key", "private key from keygen")
	dir := fs.String("dir", "release", "output folder (the release's assets)")
	ver := fs.String("version", "", "release version")
	image := fs.String("image", "", "the Pi image to publish with the release")
	fs.Parse(args)
	if *ver == "" || fs.NArg() == 0 {
		return fmt.Errorf("need -version and at least one os/arch=binary")
	}
	m := upgrade.Manifest{Version: *ver, Files: map[string]string{}, SHA256: map[string]string{}}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	for _, a := range fs.Args() {
		plat, src, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("bad binary argument %q", a)
		}
		name := "hub-" + strings.ReplaceAll(plat, "/", "-")
		if strings.HasPrefix(plat, "windows/") {
			name += ".exe"
		}
		sum, err := copyAsset(src, filepath.Join(*dir, name))
		if err != nil {
			return err
		}
		m.Files[plat], m.SHA256[plat] = name, sum
	}
	if *image != "" {
		name := filepath.Base(*image)
		sum, err := copyAsset(*image, filepath.Join(*dir, name))
		if err != nil {
			return err
		}
		m.Image = &upgrade.Image{File: name, SHA256: sum}
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(*dir, "release.json"), raw, 0o644); err != nil {
		return err
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
	return os.WriteFile(filepath.Join(*dir, "release.json.sig"), []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}

// copyAsset copies src to dst (unless they are the same file) and returns its SHA-256.
func copyAsset(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	h := sha256.New()
	sa, _ := filepath.Abs(src)
	da, _ := filepath.Abs(dst)
	if sa == da {
		_, err = io.Copy(h, in)
		return hex.EncodeToString(h.Sum(nil)), err
	}
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), out.Close()
}
