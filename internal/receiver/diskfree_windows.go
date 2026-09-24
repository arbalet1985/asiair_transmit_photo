package receiver

import "golang.org/x/sys/windows"

// diskSpace returns the free space available to this user and the size of
// the disk holding dir.
func diskSpace(dir string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, err
	}
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, nil)
	return free, total, err
}
