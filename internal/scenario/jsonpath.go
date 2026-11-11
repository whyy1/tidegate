package scenario

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// JSONPath is a compiled path of the supported JSONPath subset:
// $ (root), .key, ['key'], ["key"], [index] and [-index]. Lookup scans the
// raw document and skips everything off the path, so it allocates nothing
// and never decodes values it does not return.
type JSONPath struct {
	src  string
	segs []pathSeg
}

type pathSeg struct {
	key   string
	index int
	isIdx bool
}

// String returns the source expression.
func (p *JSONPath) String() string { return p.src }

// CompileJSONPath parses expr. A missing leading `$` is allowed.
func CompileJSONPath(expr string) (*JSONPath, error) {
	src := strings.TrimSpace(expr)
	if src == "" {
		return nil, fmt.Errorf("JSONPath 为空")
	}
	rest := src
	if rest[0] == '$' {
		rest = rest[1:]
	} else if rest[0] != '.' && rest[0] != '[' {
		rest = "." + rest
	}
	p := &JSONPath{src: src}
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			if strings.HasPrefix(rest, ".") {
				return nil, fmt.Errorf("JSONPath %q：不支持递归查找 ..", src)
			}
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			name := rest[:end]
			if name == "" {
				return nil, fmt.Errorf("JSONPath %q：. 后面缺少字段名", src)
			}
			if name == "*" {
				return nil, fmt.Errorf("JSONPath %q：不支持通配符 *", src)
			}
			p.segs = append(p.segs, pathSeg{key: name})
			rest = rest[end:]
		case '[':
			rest = rest[1:]
			if rest != "" && (rest[0] == '\'' || rest[0] == '"') {
				q := rest[0]
				var key strings.Builder
				i := 1
				for ; i < len(rest) && rest[i] != q; i++ {
					if rest[i] == '\\' && i+1 < len(rest) {
						i++
					}
					key.WriteByte(rest[i])
				}
				if i >= len(rest) || i+1 >= len(rest) || rest[i+1] != ']' {
					return nil, fmt.Errorf("JSONPath %q：引号或方括号没有闭合", src)
				}
				p.segs = append(p.segs, pathSeg{key: key.String()})
				rest = rest[i+2:]
				continue
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("JSONPath %q：方括号没有闭合", src)
			}
			inner := strings.TrimSpace(rest[:end])
			if inner == "*" {
				return nil, fmt.Errorf("JSONPath %q：不支持通配符 [*]", src)
			}
			n, err := strconv.Atoi(inner)
			if err != nil {
				return nil, fmt.Errorf("JSONPath %q：下标 %q 不是整数（不支持过滤表达式和切片）", src, inner)
			}
			p.segs = append(p.segs, pathSeg{index: n, isIdx: true})
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("JSONPath %q：无法识别 %q", src, rest)
		}
	}
	return p, nil
}

// Lookup returns the raw JSON value at the path.
func (p *JSONPath) Lookup(doc []byte) ([]byte, bool) {
	i := skipWS(doc, 0)
	for _, s := range p.segs {
		if i >= len(doc) {
			return nil, false
		}
		var ok bool
		if s.isIdx {
			if doc[i] != '[' {
				return nil, false
			}
			i, ok = arrayElem(doc, i, s.index)
		} else {
			if doc[i] != '{' {
				return nil, false
			}
			i, ok = objectMember(doc, i, s.key)
		}
		if !ok {
			return nil, false
		}
	}
	end, ok := skipValue(doc, i)
	if !ok {
		return nil, false
	}
	return doc[i:end], true
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipString expects b[i] == '"' and returns the index past the closing quote.
func skipString(b []byte, i int) (int, bool) {
	for i++; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return i, false
}

// skipValue returns the index just past the value starting at b[i].
func skipValue(b []byte, i int) (int, bool) {
	i = skipWS(b, i)
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				var ok bool
				if i, ok = skipString(b, i); !ok {
					return i, false
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return i, false
	default:
		start := i
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, i > start
			}
			i++
		}
		return i, i > start
	}
}

func keyEquals(raw []byte, key string) bool {
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw) == key
	}
	dec, ok := appendUnquoted(nil, raw)
	return ok && string(dec) == key
}

// objectMember expects b[i] == '{' and returns the start of the value of key.
func objectMember(b []byte, i int, key string) (int, bool) {
	i++
	for {
		i = skipWS(b, i)
		if i >= len(b) || b[i] != '"' {
			return i, false
		}
		ks := i
		ke, ok := skipString(b, i)
		if !ok {
			return i, false
		}
		match := keyEquals(b[ks+1:ke-1], key)
		i = skipWS(b, ke)
		if i >= len(b) || b[i] != ':' {
			return i, false
		}
		i = skipWS(b, i+1)
		if match {
			return i, true
		}
		if i, ok = skipValue(b, i); !ok {
			return i, false
		}
		i = skipWS(b, i)
		if i >= len(b) || b[i] != ',' {
			return i, false
		}
		i++
	}
}

// arrayElem expects b[i] == '[' and returns the start of element idx.
// Negative indexes count from the end.
func arrayElem(b []byte, i int, idx int) (int, bool) {
	if idx < 0 {
		n, ok := arrayLen(b, i)
		if !ok || n+idx < 0 {
			return i, false
		}
		idx += n
	}
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == ']' {
		return i, false
	}
	for k := 0; ; k++ {
		i = skipWS(b, i)
		if k == idx {
			return i, i < len(b)
		}
		var ok bool
		if i, ok = skipValue(b, i); !ok {
			return i, false
		}
		i = skipWS(b, i)
		if i >= len(b) || b[i] != ',' {
			return i, false
		}
		i++
	}
}

func arrayLen(b []byte, i int) (int, bool) {
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == ']' {
		return 0, true
	}
	n := 0
	for {
		var ok bool
		if i, ok = skipValue(b, i); !ok {
			return 0, false
		}
		n++
		i = skipWS(b, i)
		if i >= len(b) {
			return 0, false
		}
		if b[i] == ']' {
			return n, true
		}
		if b[i] != ',' {
			return 0, false
		}
		i++
	}
}

// AppendJSONText appends the text of a raw JSON value: strings are
// unquoted, every other value is copied verbatim.
func AppendJSONText(dst, raw []byte) ([]byte, bool) {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return appendUnquoted(dst, raw[1:len(raw)-1])
	}
	return append(dst, raw...), true
}

// appendUnquoted decodes the escapes of a JSON string body.
func appendUnquoted(dst, s []byte) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			dst = append(dst, c)
			continue
		}
		i++
		if i >= len(s) {
			return dst, false
		}
		switch s[i] {
		case '"', '\\', '/':
			dst = append(dst, s[i])
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, ok := hex4(s, i+1)
			if !ok {
				return dst, false
			}
			i += 4
			if utf16.IsSurrogate(r) {
				if i+6 < len(s)+0 && s[i+1] == '\\' && s[i+2] == 'u' {
					if r2, ok := hex4(s, i+3); ok {
						if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
							r = dec
							i += 6
						}
					}
				}
			}
			dst = utf8.AppendRune(dst, r)
		default:
			return dst, false
		}
	}
	return dst, true
}

func hex4(s []byte, i int) (rune, bool) {
	if i+4 > len(s) {
		return 0, false
	}
	var r rune
	for _, c := range s[i : i+4] {
		r <<= 4
		switch {
		case '0' <= c && c <= '9':
			r |= rune(c - '0')
		case 'a' <= c && c <= 'f':
			r |= rune(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			r |= rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return r, true
}
