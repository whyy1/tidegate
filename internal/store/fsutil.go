package store

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"

	"tidegate/internal/safepath"
)

const (
	// tempPrefix marks temporary files and directories. Names starting with
	// a dot are never produced from display names and are skipped by every
	// directory scan.
	tempPrefix = ".tmp-"
	// trashPrefix marks directories renamed away before removal.
	trashPrefix = ".deleted-"

	dirPerm  = 0o750
	filePerm = 0o600

	// maxStemBytes bounds names derived from display names, leaving room for
	// collision suffixes and extensions within the 255 byte limit of common
	// file systems.
	maxStemBytes = 120
	// maxFileNameBytes bounds data file names.
	maxFileNameBytes = safepath.MaxFileNameBytes
)

var utf8BOM = []byte("\xef\xbb\xbf")

// newID returns a new ULID string.
func newID() string { return ulid.Make().String() }

// normalizeID returns the canonical (upper case) form of a ULID, or false if
// s is not a ULID. IDs coming from clients are only used in paths after
// passing through this function, so they can never contain separators.
func normalizeID(s string) (string, bool) {
	id, err := ulid.ParseStrict(s)
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// versionOf returns the version token of file content.
func versionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// randomSuffix returns a short random string for temporary names.
func randomSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeFileAtomic writes data to a temporary file in the target directory,
// flushes it and renames it over path, so readers see either the old or the
// new content and a crash never leaves a half written file.
func writeFileAtomic(path string, data []byte) error {
	return writeAtomic(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// writeAtomic is writeFileAtomic for streamed content.
func writeAtomic(path string, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmp := f.Name()
	if err := fill(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入文件失败：%w", err)
	}
	if err := renameRetry(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("保存文件失败：%w", err)
	}
	syncDir(dir)
	return nil
}

// renameRetry renames a file or directory. On Windows a rename fails while
// another process (a virus scanner, an indexer, an editor) holds the file
// open, so it is retried for a short while.
func renameRetry(from, to string) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

// syncDir flushes a directory entry change where the platform supports it.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// removeAllRenamed moves a directory out of the way under a hidden name and
// then removes it, so that a partially failed removal never leaves a half
// deleted item visible.
func removeAllRenamed(dir string) error {
	trash := filepath.Join(filepath.Dir(dir), trashPrefix+filepath.Base(dir)+"-"+randomSuffix())
	if err := renameRetry(dir, trash); err != nil {
		return fmt.Errorf("删除失败：%w", err)
	}
	_ = os.RemoveAll(trash)
	return nil
}

// marshalYAML encodes v with two space indentation.
func marshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeYAML atomically writes v as YAML and returns the new version.
func writeYAML(path string, v any) (string, error) {
	data, err := marshalYAML(v)
	if err != nil {
		return "", fmt.Errorf("编码 YAML 失败：%w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return versionOf(data), nil
}

// readYAML decodes a YAML file into v and returns its version. Unknown
// fields are ignored so that files written by newer versions still load.
func readYAML(path string, v any) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := yaml.Unmarshal(bytes.TrimPrefix(data, utf8BOM), v); err != nil {
		return "", fmt.Errorf("解析 %s 失败：%s", filepath.Base(path), strings.TrimPrefix(err.Error(), "yaml: "))
	}
	return versionOf(data), nil
}

// isWindowsReserved reports whether a file name maps to a device on Windows.
// The part before the first dot counts, trailing spaces ignored.
func isWindowsReserved(name string) bool {
	return safepath.IsWindowsReserved(name)
}

func isInvalidNameRune(r rune) bool {
	return safepath.IsInvalidNameRune(r)
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// fileStem turns a display name into a portable file name stem: characters
// that are invalid on Windows or Linux become '_', leading dots and trailing
// dots and spaces are removed, device names get a '_' prefix and the length
// is bounded.
func fileStem(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if isInvalidNameRune(r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	s := strings.TrimLeft(b.String(), ". ")
	s = truncateUTF8(s, maxStemBytes)
	s = strings.TrimRight(s, ". ")
	if s == "" {
		s = "untitled"
	}
	if isWindowsReserved(s) {
		s = "_" + s
	}
	return s
}

// uniqueName returns stem+ext, or stem-N+ext for the smallest N >= 2 that
// is not taken. taken holds lower case names, because Windows and macOS
// compare file names case-insensitively.
func uniqueName(stem, ext string, taken map[string]bool) string {
	name := stem + ext
	for n := 2; taken[strings.ToLower(name)]; n++ {
		name = stem + "-" + strconv.Itoa(n) + ext
	}
	return name
}

// ValidateFileName checks a data file name supplied by a client. Only
// plain names inside the project's file directory are accepted: no
// separators, no parent references, no absolute or volume paths, no hidden
// names and no Windows device names.
func ValidateFileName(name string) error {
	if err := safepath.ValidateFileName(name); err != nil {
		return invalid("name", "%s", err.Error())
	}
	return nil
}

// joinInside joins a validated name to dir and double checks that the
// result stays directly inside dir.
func joinInside(dir, name string) (string, error) {
	if err := ValidateFileName(name); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != filepath.Clean(dir) {
		return "", invalid("name", "文件名无效")
	}
	return p, nil
}
