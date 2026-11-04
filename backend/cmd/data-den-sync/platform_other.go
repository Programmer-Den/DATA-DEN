//go:build !windows

package main

import "errors"

func localName(string) bool { return true }

// seal keeps the app password as it is; the file is readable only by the
// user (0600).
func seal(plain []byte) ([]byte, error) { return plain, nil }

func unseal(sealed []byte) ([]byte, error) { return sealed, nil }

func hideWindow() {}

func autostart(bool) error {
	return errors.New("autostart gibt es nur unter Windows; sonst data-den-sync run über systemd --user, launchd oder cron starten")
}
