package auth

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := hashPassword(testPassword, testParams)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=64,t=1,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(h) {
		t.Fatalf("unexpected PHC string %q", h)
	}
	if ok, err := verifyPassword(testPassword, h); err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	if ok, err := verifyPassword(testPassword+"x", h); err != nil || ok {
		t.Fatalf("verify wrong password: ok=%v err=%v", ok, err)
	}
	h2, _ := hashPassword(testPassword, testParams)
	if h == h2 {
		t.Fatal("two hashes of the same password must use different salts")
	}
}

func TestParsePHCRejectsMalformed(t *testing.T) {
	good, _ := hashPassword(testPassword, testParams)
	parts := strings.Split(good, "$")
	salt, key := parts[4], parts[5]
	cases := map[string]string{
		"empty":          "",
		"argon2i":        "$argon2i$v=19$m=64,t=1,p=1$" + salt + "$" + key,
		"old version":    "$argon2id$v=16$m=64,t=1,p=1$" + salt + "$" + key,
		"param order":    "$argon2id$v=19$t=1,m=64,p=1$" + salt + "$" + key,
		"missing param":  "$argon2id$v=19$m=64,t=1$" + salt + "$" + key,
		"huge memory":    "$argon2id$v=19$m=4294967295,t=1,p=1$" + salt + "$" + key,
		"zero time":      "$argon2id$v=19$m=64,t=0,p=1$" + salt + "$" + key,
		"zero threads":   "$argon2id$v=19$m=64,t=1,p=0$" + salt + "$" + key,
		"bad base64":     "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$***",
		"padded base64":  "$argon2id$v=19$m=64,t=1,p=1$" + salt + "=$" + key,
		"short salt":     "$argon2id$v=19$m=64,t=1,p=1$AAAA$" + key,
		"short key":      "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$AAAA",
		"extra segment":  good + "$x",
		"no leading sep": strings.TrimPrefix(good, "$"),
	}
	for name, h := range cases {
		if _, _, _, err := parsePHC(h); !errors.Is(err, errMalformedHash) {
			t.Errorf("%s: err = %v, want errMalformedHash", name, err)
		}
		if ok, _ := verifyPassword(testPassword, h); ok {
			t.Errorf("%s: verify must fail", name)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	h, _ := hashPassword(testPassword, testParams)
	if needsRehash(h, testParams) {
		t.Fatal("same parameters must not need a rehash")
	}
	stronger := testParams
	stronger.Time = 2
	if !needsRehash(h, stronger) {
		t.Fatal("different time cost must need a rehash")
	}
	longer := testParams
	longer.KeyLen = 64
	if !needsRehash(h, longer) {
		t.Fatal("different key length must need a rehash")
	}
	if !needsRehash("garbage", testParams) {
		t.Fatal("unparsable hash must need a rehash")
	}
}

func TestArgon2ParamsValidate(t *testing.T) {
	if err := DefaultArgon2Params().validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Argon2Params{
		{Memory: 64, Time: 1, Threads: 0, SaltLen: 16, KeyLen: 32},
		{Memory: 4, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32},
		{Memory: 64, Time: 0, Threads: 1, SaltLen: 16, KeyLen: 32},
		{Memory: 64, Time: 1, Threads: 1, SaltLen: 4, KeyLen: 32},
		{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 8},
	}
	for _, p := range bad {
		if p.validate() == nil {
			t.Errorf("%+v must be rejected", p)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	ok := []string{
		"correct horse battery",
		"Tr0ub4dor&3x",
		"潮汐之门安全密码一号",
		"plainlowercasewords",
	}
	for _, p := range ok {
		if err := checkPasswordPolicy(p, "admin", 10); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	bad := map[string]string{
		"short":            "abc123xyz",
		"short cjk":        "密码太短了",
		"too long":         strings.Repeat("ab1", MaxPasswordBytes),
		"same as username": "Administrator",
		"reversed name":    "rotartsinimda",
		"repeated char":    "aaaaaaaaaaaa",
		"repeated unit":    "abcdabcdabcd",
		"few distinct":     "1212121212",
		"ascending":        "0123456789",
		"descending":       "9876543210",
		"letters run":      "abcdefghijk",
		"common":           "Password123",
		"keyboard":         "qwertyuiop",
		"invalid utf8":     "abcdefghij\xff",
	}
	for name, p := range bad {
		err := checkPasswordPolicy(p, "administrator", 10)
		if !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%s (%q): err = %v, want ErrWeakPassword", name, p, err)
			continue
		}
		if e, _ := AsError(err); e.Message == "" || e.Status != 400 {
			t.Errorf("%s: bad error %+v", name, e)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	for in, want := range map[string]string{
		"admin":        "admin",
		"  Alice.B_c ": "Alice.B_c",
		"ops@team-1":   "ops@team-1",
		"管理员":          "管理员",
	} {
		got, err := normalizeUsername(in)
		if err != nil || got != want {
			t.Errorf("normalizeUsername(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "a b", "x/y", "<script>", strings.Repeat("a", 65), "bad\xff"} {
		if _, err := normalizeUsername(in); !errors.Is(err, ErrInvalidUsername) {
			t.Errorf("normalizeUsername(%q) err = %v, want ErrInvalidUsername", in, err)
		}
	}
}
