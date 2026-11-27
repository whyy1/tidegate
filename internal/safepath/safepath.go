// Package safepath provides dependency-free validation helpers for user-supplied
// file names across store and distributed agent runtimes.
package safepath

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxFileNameBytes bounds plain project data file names.
const MaxFileNameBytes = 200

// InvalidNameChars lists characters prohibited in portable file names.
const InvalidNameChars = `<>:"/\|?*`

// windowsReserved lists device names that cannot be used as file names on
// Windows, with or without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"COM¹": true, "COM²": true, "COM³": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"LPT¹": true, "LPT²": true, "LPT³": true,
}

// IsWindowsReserved reports whether a file name maps to a device on Windows.
// The part before the first dot counts, trailing spaces ignored.
func IsWindowsReserved(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.TrimRight(base, " ")
	return windowsReserved[strings.ToUpper(base)]
}

// IsInvalidNameRune reports whether r is disallowed in portable file names.
func IsInvalidNameRune(r rune) bool {
	return r < 0x20 || r == 0x7f || r == utf8.RuneError || strings.ContainsRune(InvalidNameChars, r)
}

// ValidateFileName checks a data file name supplied by a client. Only plain
// names inside the project's file directory are accepted: no separators, no
// parent references, no absolute or volume paths, no hidden names, and no
// Windows device names.
func ValidateFileName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("文件名不能为空")
	case !utf8.ValidString(name):
		return fmt.Errorf("文件名不是合法的 UTF-8 文本")
	case len(name) > MaxFileNameBytes:
		return fmt.Errorf("文件名过长（最多 %d 字节）", MaxFileNameBytes)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("文件名不能以点开头")
	case filepath.IsAbs(name) || filepath.VolumeName(name) != "":
		return fmt.Errorf("文件名不能是路径")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.HasPrefix(name, " "):
		return fmt.Errorf("文件名不能以空格开头，也不能以点或空格结尾")
	case IsWindowsReserved(name):
		return fmt.Errorf("%q 是系统保留的设备名，不能用作文件名", name)
	}
	for _, r := range name {
		if IsInvalidNameRune(r) {
			return fmt.Errorf("文件名不能包含 / \\ : * ? \" < > | 或控制字符")
		}
	}
	return nil
}
