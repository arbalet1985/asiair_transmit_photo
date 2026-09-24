package source

import "strings"

// Drive is a removable disk (USB stick, memory card) of this computer.
type Drive struct {
	Root  string // e.g. `E:\`
	Label string // volume label, may be empty
}

func (d Drive) String() string {
	if d.Label == "" {
		return d.Root
	}
	return d.Root + " (" + d.Label + ")"
}

// RemovableDrives lists the inserted removable disks. It is a variable so
// tests can replace it.
var RemovableDrives = removableDrives

// FolderLink returns the "asiair" setting for a folder picked on this
// computer: the root of a disk is taken as the ASIAIR USB stick ("usb:E:\"),
// any other folder is read as is ("dir:...").
func FolderLink(folder string) string {
	f := strings.TrimRight(strings.TrimSpace(folder), `\/`)
	if len(f) == 2 && f[1] == ':' {
		return "usb:" + strings.ToUpper(f) + `\`
	}
	return "dir:" + strings.TrimSpace(folder)
}
