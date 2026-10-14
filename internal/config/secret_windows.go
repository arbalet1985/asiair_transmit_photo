package config

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protect encrypts with DPAPI for this computer (not only this user: the
// receiver runs as a service too).
func protect(b []byte) ([]byte, error) {
	return dpapi(b, true)
}

func unprotect(b []byte) ([]byte, error) {
	return dpapi(b, false)
}

func dpapi(b []byte, seal bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	var err error
	if seal {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN|windows.CRYPTPROTECT_LOCAL_MACHINE, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
