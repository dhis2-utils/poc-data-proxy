package stick

import (
	"golang.org/x/sys/windows"
)

// removableDrives lists the drives Windows reports as removable, with their labels,
// serial numbers and sizes.
func removableDrives() []Drive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) != windows.DRIVE_REMOVABLE {
			continue
		}
		label := make([]uint16, windows.MAX_PATH+1)
		var serial, maxLen, flags uint32
		if windows.GetVolumeInformation(p, &label[0], uint32(len(label)), &serial, &maxLen, &flags, nil, 0) != nil {
			continue // no medium in the drive
		}
		var avail, total, free uint64
		windows.GetDiskFreeSpaceEx(p, &avail, &total, &free)
		out = append(out, Drive{Root: root, Label: windows.UTF16ToString(label), Serial: serial, Size: total})
	}
	return out
}
