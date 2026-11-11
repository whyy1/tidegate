package scenario

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Env is the per virtual user state a template reads while rendering. The
// engine owns one Env per virtual user, so rendering needs no locks.
type Env struct {
	// Vars holds the current values of mutable variables, indexed by slot
	// (Plan.Vars).
	Vars [][]byte
	// Rows holds the current row of every dataset, indexed like Plan.Data.
	Rows [][]string
	// Rand is the virtual user's private random source.
	Rand *rand.Rand
}

type segKind uint8

const (
	segLit segKind = iota
	segVar
	segData
	segUUID
	segTimestamp
	segRandInt
	segRandString
)

type escapeMode uint8

const (
	escapeNone  escapeMode = iota
	escapeQuery            // application/x-www-form-urlencoded value
)

type segment struct {
	kind   segKind
	escape escapeMode
	lit    string
	a, b   int64
}

// Template is a pre-parsed `{{...}}` template. Rendering only appends
// literal pieces and resolved values: no scanning, no reflection.
type Template struct {
	segs   []segment
	static bool
	lit    string
	src    string
}

// Static reports whether the template has no dynamic parts.
func (t *Template) Static() bool { return t.static }

// Literal returns the rendered value of a static template.
func (t *Template) Literal() string { return t.lit }

// Source returns the original template text.
func (t *Template) Source() string { return t.src }

// UsesVar reports whether the template reads variable slot.
func (t *Template) UsesVar(slot int) bool {
	for _, s := range t.segs {
		if s.kind == segVar && int(s.a) == slot {
			return true
		}
	}
	return false
}

// Append renders the template onto dst.
func (t *Template) Append(dst []byte, env *Env) []byte {
	if t.static {
		return append(dst, t.lit...)
	}
	for i := range t.segs {
		s := &t.segs[i]
		if s.kind == segLit {
			dst = append(dst, s.lit...)
			continue
		}
		start := len(dst)
		switch s.kind {
		case segVar:
			dst = append(dst, env.Vars[s.a]...)
		case segData:
			if row := env.Rows[s.a]; row != nil {
				dst = append(dst, row[s.b]...)
			}
		case segUUID:
			dst = appendUUID(dst, env.Rand)
		case segTimestamp:
			dst = strconv.AppendInt(dst, time.Now().Unix(), 10)
		case segRandInt:
			dst = strconv.AppendInt(dst, s.a+env.Rand.Int64N(s.b-s.a+1), 10)
		case segRandString:
			dst = appendRandString(dst, env.Rand, int(s.a))
		}
		if s.escape == escapeQuery && needsQueryEscape(dst[start:]) {
			dst = escapeTail(dst, start)
		}
	}
	return dst
}

// String renders the template to a new string. Not for hot paths.
func (t *Template) String(env *Env) string { return string(t.Append(nil, env)) }

const hexDigits = "0123456789abcdef"

func appendUUID(dst []byte, r *rand.Rand) []byte {
	var u [16]byte
	a, b := r.Uint64(), r.Uint64()
	for i := 0; i < 8; i++ {
		u[i] = byte(a >> (8 * i))
		u[8+i] = byte(b >> (8 * i))
	}
	u[6] = u[6]&0x0f | 0x40 // version 4
	u[8] = u[8]&0x3f | 0x80 // RFC 4122 variant
	for i, c := range u {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			dst = append(dst, '-')
		}
		dst = append(dst, hexDigits[c>>4], hexDigits[c&0x0f])
	}
	return dst
}

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func appendRandString(dst []byte, r *rand.Rand, n int) []byte {
	for n > 0 {
		v := r.Uint64()
		// 10 characters per 64 random bits (6 bits each).
		for k := 0; k < 10 && n > 0; k++ {
			dst = append(dst, alnum[(v&63)%uint64(len(alnum))])
			v >>= 6
			n--
		}
	}
	return dst
}

// shouldEscapeQuery follows net/url.QueryEscape: unreserved characters stay.
func shouldEscapeQuery(c byte) bool {
	if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' {
		return false
	}
	switch c {
	case '-', '_', '.', '~':
		return false
	}
	return true
}

func needsQueryEscape(b []byte) bool {
	for _, c := range b {
		if shouldEscapeQuery(c) {
			return true
		}
	}
	return false
}

// escapeTail query-escapes dst[start:] in place.
func escapeTail(dst []byte, start int) []byte {
	var tmp [256]byte
	raw := append(tmp[:0], dst[start:]...)
	dst = dst[:start]
	return appendQueryEscape(dst, raw)
}

func appendQueryEscape(dst, s []byte) []byte {
	for _, c := range s {
		switch {
		case c == ' ':
			dst = append(dst, '+')
		case shouldEscapeQuery(c):
			dst = append(dst, '%', "0123456789ABCDEF"[c>>4], "0123456789ABCDEF"[c&15])
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

func queryEscape(s string) string { return string(appendQueryEscape(nil, []byte(s))) }

// templateResolver resolves names found in templates during compilation.
type templateResolver interface {
	// resolveVar returns either the expanded segments of a constant
	// variable or the slot of a mutable one.
	resolveVar(name string, escape escapeMode, depth int) ([]segment, error)
	resolveData(ds, col string) (dsIdx, colIdx int, err error)
}

type tmplError struct{ msg string }

func (e *tmplError) Error() string { return e.msg }

func errf(format string, args ...any) error {
	return &tmplError{msg: fmt.Sprintf(format, args...)}
}

// parseTemplate splits src into segments. Literal text is escaped at
// compile time; dynamic segments carry the escape mode for render time.
func parseTemplate(src string, r templateResolver, escape escapeMode, depth int) ([]segment, error) {
	var segs []segment
	addLit := func(s string) {
		if s == "" {
			return
		}
		if escape == escapeQuery {
			s = queryEscape(s)
		}
		if n := len(segs); n > 0 && segs[n-1].kind == segLit {
			segs[n-1].lit += s
			return
		}
		segs = append(segs, segment{kind: segLit, lit: s})
	}
	rest := src
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			addLit(rest)
			break
		}
		addLit(rest[:i])
		j := strings.Index(rest[i+2:], "}}")
		if j < 0 {
			return nil, errf("模板 %q 中的 {{ 没有对应的 }}", src)
		}
		expr := strings.TrimSpace(rest[i+2 : i+2+j])
		rest = rest[i+2+j+2:]
		if expr == "" {
			return nil, errf("模板 %q 中有空的 {{}}", src)
		}
		switch {
		case expr[0] == '$':
			seg, err := parseFunc(expr)
			if err != nil {
				return nil, err
			}
			seg.escape = escape
			segs = append(segs, seg)
		case strings.Contains(expr, "."):
			ds, col, _ := strings.Cut(expr, ".")
			di, ci, err := r.resolveData(strings.TrimSpace(ds), strings.TrimSpace(col))
			if err != nil {
				return nil, err
			}
			segs = append(segs, segment{kind: segData, escape: escape, a: int64(di), b: int64(ci)})
		default:
			sub, err := r.resolveVar(expr, escape, depth)
			if err != nil {
				return nil, err
			}
			for _, s := range sub {
				if s.kind == segLit {
					// Already escaped by the variable's own parse.
					if n := len(segs); n > 0 && segs[n-1].kind == segLit {
						segs[n-1].lit += s.lit
						continue
					}
				}
				segs = append(segs, s)
			}
		}
	}
	return segs, nil
}

func parseFunc(expr string) (segment, error) {
	name, args, hasArgs := strings.Cut(expr, "(")
	name = strings.TrimSpace(name)
	var params []string
	if hasArgs {
		if !strings.HasSuffix(args, ")") {
			return segment{}, errf("函数 %s 缺少右括号", expr)
		}
		args = strings.TrimSpace(strings.TrimSuffix(args, ")"))
		if args != "" {
			for _, p := range strings.Split(args, ",") {
				params = append(params, strings.TrimSpace(p))
			}
		}
	}
	argInts := func(n int) ([]int64, error) {
		if len(params) != n {
			return nil, errf("函数 %s 需要 %d 个参数", name, n)
		}
		out := make([]int64, n)
		for i, p := range params {
			v, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, errf("函数 %s 的参数 %q 不是整数", name, p)
			}
			out[i] = v
		}
		return out, nil
	}
	switch name {
	case "$uuid":
		if len(params) != 0 {
			return segment{}, errf("函数 $uuid 不接受参数")
		}
		return segment{kind: segUUID}, nil
	case "$timestamp":
		if len(params) != 0 {
			return segment{}, errf("函数 $timestamp 不接受参数")
		}
		return segment{kind: segTimestamp}, nil
	case "$randomInt":
		v, err := argInts(2)
		if err != nil {
			return segment{}, err
		}
		if v[0] > v[1] {
			return segment{}, errf("$randomInt(%d,%d)：最小值大于最大值", v[0], v[1])
		}
		if v[1]-v[0] < 0 || v[1]-v[0] == 1<<63-1 {
			return segment{}, errf("$randomInt 的范围过大")
		}
		return segment{kind: segRandInt, a: v[0], b: v[1]}, nil
	case "$randomString":
		v, err := argInts(1)
		if err != nil {
			return segment{}, err
		}
		if v[0] < 1 || v[0] > 4096 {
			return segment{}, errf("$randomString 的长度必须在 1 到 4096 之间")
		}
		return segment{kind: segRandString, a: v[0]}, nil
	}
	return segment{}, errf("未知的内置函数 %s（支持 $uuid、$timestamp、$randomInt(min,max)、$randomString(n)）", name)
}

func newTemplate(src string, segs []segment) *Template {
	t := &Template{segs: segs, src: src}
	switch {
	case len(segs) == 0:
		t.static = true
	case len(segs) == 1 && segs[0].kind == segLit:
		t.static, t.lit = true, segs[0].lit
	}
	return t
}

// StaticTemplate returns a template that always renders s.
func StaticTemplate(s string) *Template {
	return &Template{static: true, lit: s, src: s, segs: []segment{{kind: segLit, lit: s}}}
}

// concatTemplates joins templates into one.
func concatTemplates(parts ...*Template) *Template {
	var segs []segment
	var src strings.Builder
	for _, p := range parts {
		src.WriteString(p.src)
		for _, s := range p.segs {
			if s.kind == segLit && len(segs) > 0 && segs[len(segs)-1].kind == segLit {
				segs[len(segs)-1].lit += s.lit
				continue
			}
			segs = append(segs, s)
		}
	}
	return newTemplate(src.String(), segs)
}
