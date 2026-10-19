package upgrade

import (
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
)

// DKEnv carries the data key to a new process: "stdin" (migrations, on every system)
// or an inherited file descriptor (the Unix re-exec, reexec_unix.go). On Windows the
// re-exec hands it over in a sealed file instead (handover.go).
const DKEnv = "HUB_DK_FD"

// HandedOverDK reads a data key passed by Reexec, if any.
func HandedOverDK() []byte {
	s := os.Getenv(DKEnv)
	if s == "" {
		return handedOverByFile()
	}
	os.Unsetenv(DKEnv)
	if s == "stdin" {
		return decodeDK(os.Stdin)
	}
	fd, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), "dk")
	defer f.Close()
	return decodeDK(f)
}

func decodeDK(r io.Reader) []byte {
	b, err := io.ReadAll(io.LimitReader(r, 1024))
	if err != nil {
		return nil
	}
	dk, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	return dk
}
