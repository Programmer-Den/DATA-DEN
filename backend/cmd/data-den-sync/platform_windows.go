package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/desktop"
)

func localName(name string) bool { return desktop.WindowsName(name) }

// seal encrypts the app password with DPAPI, so only this Windows user can
// read it.
func seal(plain []byte) ([]byte, error) {
	return dpapi(plain, true)
}

func unseal(sealed []byte) ([]byte, error) {
	return dpapi(sealed, false)
}

func dpapi(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]} //nolint:gosec // a small token
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	// DPAPI allocates the result, which LocalFree releases.
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }() //nolint:gosec // DPAPI's own buffer
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil                  //nolint:gosec // DPAPI's own buffer
}

// hideWindow closes the console window "run --hidden" was started in.
func hideWindow() {
	_, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// autostart adds or removes "data-den-sync run --hidden" at login, for
// this Windows user only.
func autostart(on bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()
	if !on {
		if err := key.DeleteValue("DATA-DEN Sync"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		fmt.Println("Autostart aus")
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := key.SetStringValue("DATA-DEN Sync", `"`+exe+`" run --hidden`); err != nil {
		return err
	}
	fmt.Println("Autostart an: die Synchronisation startet bei jeder Anmeldung")
	return nil
}
