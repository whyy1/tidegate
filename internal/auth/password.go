package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the Argon2id cost parameters. They are encoded into every
// stored hash, so changing them only affects new hashes; existing hashes are
// upgraded on the next successful login.
type Argon2Params struct {
	Memory  uint32 // memory cost in KiB
	Time    uint32 // number of passes
	Threads uint8  // degree of parallelism
	SaltLen uint32 // salt length in bytes
	KeyLen  uint32 // derived key length in bytes
}

// DefaultArgon2Params returns the production parameters: 64 MiB, 3 passes,
// 4 lanes, 16-byte salt and 32-byte key.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 4, SaltLen: 16, KeyLen: 32}
}

// Upper bounds applied both to configured parameters and to parameters
// parsed from stored hashes, so a tampered hash cannot exhaust memory.
const (
	maxArgon2Memory  = 1 << 20 // 1 GiB in KiB
	maxArgon2Time    = 64
	minArgon2SaltLen = 8
	maxArgon2SaltLen = 64
	minArgon2KeyLen  = 16
	maxArgon2KeyLen  = 64
)

func (p Argon2Params) validate() error {
	switch {
	case p.Threads < 1:
		return errors.New("argon2 threads must be at least 1")
	case p.Time < 1 || p.Time > maxArgon2Time:
		return fmt.Errorf("argon2 time must be between 1 and %d", maxArgon2Time)
	case p.Memory < 8*uint32(p.Threads) || p.Memory > maxArgon2Memory:
		return fmt.Errorf("argon2 memory must be between 8*threads and %d KiB", maxArgon2Memory)
	case p.SaltLen < minArgon2SaltLen || p.SaltLen > maxArgon2SaltLen:
		return fmt.Errorf("argon2 salt length must be between %d and %d", minArgon2SaltLen, maxArgon2SaltLen)
	case p.KeyLen < minArgon2KeyLen || p.KeyLen > maxArgon2KeyLen:
		return fmt.Errorf("argon2 key length must be between %d and %d", minArgon2KeyLen, maxArgon2KeyLen)
	}
	return nil
}

var phcB64 = base64.RawStdEncoding

// hashPassword returns a PHC string:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>
func hashPassword(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read random salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		phcB64.EncodeToString(salt), phcB64.EncodeToString(key)), nil
}

var errMalformedHash = errors.New("auth: malformed password hash")

// parsePHC decodes an Argon2id PHC string. SaltLen and KeyLen of the
// returned params reflect the stored salt and key.
func parsePHC(encoded string) (p Argon2Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errMalformedHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, errMalformedHash
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return p, nil, nil, errMalformedHash
	}
	m, okM := parseParam(fields[0], "m=", 32)
	t, okT := parseParam(fields[1], "t=", 32)
	th, okP := parseParam(fields[2], "p=", 8)
	if !okM || !okT || !okP {
		return p, nil, nil, errMalformedHash
	}
	if salt, err = phcB64.Strict().DecodeString(parts[4]); err != nil {
		return p, nil, nil, errMalformedHash
	}
	if key, err = phcB64.Strict().DecodeString(parts[5]); err != nil {
		return p, nil, nil, errMalformedHash
	}
	p = Argon2Params{
		Memory:  uint32(m),
		Time:    uint32(t),
		Threads: uint8(th),
		SaltLen: uint32(len(salt)),
		KeyLen:  uint32(len(key)),
	}
	if p.validate() != nil {
		return Argon2Params{}, nil, nil, errMalformedHash
	}
	return p, salt, key, nil
}

func parseParam(field, prefix string, bits int) (uint64, bool) {
	v, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, bits)
	return n, err == nil
}

// verifyPassword reports whether password matches the PHC hash. The final
// comparison runs in constant time.
func verifyPassword(password, encoded string) (bool, error) {
	p, salt, key, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// needsRehash reports whether a stored hash was made with parameters that
// differ from want.
func needsRehash(encoded string, want Argon2Params) bool {
	p, _, _, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return p != want
}

// MaxPasswordBytes bounds password length so that hashing cost stays
// predictable.
const MaxPasswordBytes = 1024

// checkPasswordPolicy enforces the password rules: a minimum length in
// characters, a maximum length in bytes, not equal to the username, not a
// trivially repetitive or sequential string, and not a well-known password.
// Composition rules (digits, symbols...) are intentionally not required.
func checkPasswordPolicy(password, username string, minLen int) error {
	if !utf8.ValidString(password) {
		return weakPassword("密码包含无效字符")
	}
	if n := utf8.RuneCountInString(password); n < minLen {
		return weakPassword(fmt.Sprintf("密码至少需要 %d 个字符", minLen))
	}
	if len(password) > MaxPasswordBytes {
		return weakPassword(fmt.Sprintf("密码不能超过 %d 个字节", MaxPasswordBytes))
	}
	lower := strings.ToLower(password)
	if u := strings.ToLower(strings.TrimSpace(username)); u != "" && (lower == u || lower == reverse(u)) {
		return weakPassword("密码不能与用户名相同")
	}
	if isTrivial(lower) {
		return weakPassword("密码过于简单（重复或连续的字符），请换一个")
	}
	if _, ok := commonPasswords[lower]; ok {
		return weakPassword("这个密码太常见，请换一个")
	}
	return nil
}

// isTrivial reports strings made of very few distinct characters, a short
// repeated unit, or a single ascending or descending run such as
// "0123456789" or "abcdefghij".
func isTrivial(s string) bool {
	r := []rune(s)
	if len(r) == 0 {
		return true
	}
	distinct := make(map[rune]struct{}, len(r))
	for _, c := range r {
		distinct[c] = struct{}{}
	}
	if len(distinct) < 4 {
		return true
	}
	for period := 1; period <= 4 && 2*period <= len(r); period++ {
		repeated := true
		for i := period; i < len(r); i++ {
			if r[i] != r[i-period] {
				repeated = false
				break
			}
		}
		if repeated {
			return true
		}
	}
	step := r[1] - r[0]
	if step == 1 || step == -1 {
		run := true
		for i := 2; i < len(r); i++ {
			if r[i]-r[i-1] != step {
				run = false
				break
			}
		}
		if run {
			return true
		}
	}
	return false
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// commonPasswords lists frequently used passwords that satisfy the length
// rule. Entries are lower case; lookups use the lower-cased password.
var commonPasswords = func() map[string]struct{} {
	list := []string{
		"1234567890", "0987654321", "12345678910", "123456789a", "a123456789",
		"123456789q", "q123456789", "1234567890a", "a1234567890", "123456789abc",
		"abc123456789", "abcd123456", "abcd1234567", "abcd12345678", "abc1234567",
		"aa123456789", "aaa123456789", "qq123456789", "qwertyuiop", "qwertyuiop1",
		"qwerty1234", "qwerty12345", "qwerty123456", "1qaz2wsx3edc", "1qazxsw23edc",
		"zaq12wsxcde3", "qazwsxedcrfv", "1q2w3e4r5t", "1q2w3e4r5t6y", "q1w2e3r4t5",
		"q1w2e3r4t5y6", "asdfghjkl1", "asdfghjkl;", "zxcvbnm123", "zxcvbnm1234",
		"1qaz2wsx3e", "password12", "password123", "password1234", "passw0rd12",
		"passw0rd123", "p@ssw0rd12", "p@ssw0rd123", "p@ssword123", "password!",
		"password12!", "password123!", "iloveyou12", "iloveyou123", "football123",
		"baseball123", "superman123", "princess123", "sunshine123", "welcome123",
		"welcome1234", "letmein123", "trustno1234", "administrator", "admin12345",
		"admin123456", "admin@1234", "admin@12345", "admin123!@#", "root123456",
		"root@12345", "changeme123", "default123", "test123456", "test1234567",
		"testtest12", "woaini1314", "woaini5201314", "5201314520", "1314520520",
		"wang123456", "zhang123456", "li123456789", "a5201314", "qwer123456",
		"qwer1234567", "asdf123456", "asdf1234567", "zxcv123456", "1qaz@wsx3edc",
		"1qaz!qaz2wsx", "tidegate123", "tidegate1234", "tidegate12345", "tidegate2026",
		"tidegate@123", "loadtest123", "123qweasdzxc", "qweasdzxc123", "1234qwerasdf",
		"1234abcd5678", "11223344556", "1122334455", "1234554321", "9876543210a",
	}
	m := make(map[string]struct{}, len(list))
	for _, s := range list {
		m[s] = struct{}{}
	}
	return m
}()

// maxUsernameRunes bounds usernames.
const maxUsernameRunes = 64

// normalizeUsername trims surrounding spaces and validates the result:
// 1 to 64 characters, letters, digits and . _ - @ only.
func normalizeUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalidUsername("用户名不能为空")
	}
	if !utf8.ValidString(name) {
		return "", invalidUsername("用户名包含无效字符")
	}
	if utf8.RuneCountInString(name) > maxUsernameRunes {
		return "", invalidUsername(fmt.Sprintf("用户名不能超过 %d 个字符", maxUsernameRunes))
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-@", r) {
			continue
		}
		return "", invalidUsername("用户名只能包含字母、数字以及 . _ - @")
	}
	return name, nil
}
