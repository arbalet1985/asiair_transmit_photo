package main

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var shFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shFileOpStruct is SHFILEOPSTRUCTW as laid out on 64-bit Windows.
type shFileOpStruct struct {
	hwnd          uintptr
	wFunc         uint32
	pFrom         *uint16
	pTo           *uint16
	fFlags        uint16
	anyAborted    int32
	nameMappings  uintptr
	progressTitle *uint16
}

const (
	foDelete          = 0x0003
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

// recycle moves the files to the Recycle Bin (where the drive has one: on a
// network folder Windows deletes them for good). The files left are told by
// the caller checking them.
func recycle(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		// the structure is packed otherwise on 32-bit Windows: delete for good
		var errs []error
		for _, p := range paths {
			errs = append(errs, os.Remove(p))
		}
		return errors.Join(errs...)
	}
	// the list: every path ends with a zero, the list with one more
	var from []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return err
		}
		from = append(from, u...)
	}
	from = append(from, 0)
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r, _, _ := shFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return errors.New("Windows не удалил часть файлов (код " + windows.Errno(r).Error() + ")")
	}
	if op.anyAborted != 0 {
		return errors.New("удаление прервано")
	}
	return nil
}
