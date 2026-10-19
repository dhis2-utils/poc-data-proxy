package stick

import (
	"fmt"
	"os"
	"path/filepath"
)

// Drive is a removable drive (Windows, usb_dir: auto), where the hub may keep its copy.
type Drive struct {
	Root   string // E:\
	Label  string
	Serial uint32
	Size   uint64
}

// ID names the drive across plugs: its volume label and serial number.
func (d Drive) ID() string { return fmt.Sprintf("%s-%08X", d.Label, d.Serial) }

// Removable lists the removable drives plugged in (none on Linux, where usb_dir is a
// mount point). A variable, so tests can plug in folders.
var Removable = removableDrives

// Locate finds the hub's stick among the drives: the one a person chose (found by its
// label and serial, whatever its letter), else, before any choice, one holding the
// hub's folder. Never a guess: with no choice
// made it returns the drives to ask about, and when the chosen one is gone it reports
// it missing rather than use another.
func Locate(drives []Drive, hubID, choice string, has func(root string) bool) (root string, ask []Drive, missing bool) {
	if choice != "" { // a person's choice wins, even over an older stick of this hub
		for _, d := range drives {
			if d.ID() == choice {
				return d.Root, nil, false
			}
		}
		return "", nil, true
	}
	for _, d := range drives {
		if has(d.Root) {
			return d.Root, nil, false
		}
	}
	return "", drives, false
}

// hasHub reports whether root holds this hub's copy.
func hasHub(hubID string) func(root string) bool {
	return func(root string) bool {
		return dirExists(filepath.Join(root, "facility-hub", hubID))
	}
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
