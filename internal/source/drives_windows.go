package source

import "golang.org/x/sys/windows"

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
		// fails for an empty card reader slot: skip it
		if err := windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, nil, 0); err != nil {
			continue
		}
		out = append(out, Drive{Root: root, Label: windows.UTF16ToString(label)})
	}
	return out
}
