//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"runtime"
)

func localName(string) bool { return true }

// seal keeps the app password as it is; the file is readable only by the
// user (0600).
func seal(plain []byte) ([]byte, error) { return plain, nil }

func unseal(sealed []byte) ([]byte, error) { return sealed, nil }

func hideWindow() {}

func autostart(bool) error {
	return errors.New("autostart gibt es nur unter Windows; sonst data-den-sync run über systemd --user, launchd oder cron starten")
}

// openFile opens name with the program the desktop has for it.
func openFile(name string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, name).Start() //nolint:gosec // a file the user asked to open
}
