package desktop

import "strings"

// reservedWindows are names Windows keeps for devices, also with any
// extension, such as "con.txt".
var reservedWindows = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// WindowsName reports whether Windows can hold a file or folder of this
// name.
func WindowsName(name string) bool {
	if name == "" || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, c := range name {
		if c < 32 || strings.ContainsRune(`<>:"/\|?*`, c) {
			return false
		}
	}
	stem, _, _ := strings.Cut(name, ".")
	return !reservedWindows[strings.ToUpper(strings.TrimRight(stem, " "))]
}
