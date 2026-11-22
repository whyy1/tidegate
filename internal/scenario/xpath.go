package scenario

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// XPath is a pre-compiled XML/HTML path expression used by extractors.
// Supports absolute paths (/root/item), descendant searches (//item),
// 1-based positional predicates ([1]), attribute equality predicates
// ([@name='token']), leaf text (/text()), and attributes (/@id or @id).
type XPath struct {
	src   string
	steps []xpathStep
	attr  string
}

type xpathStep struct {
	recursive bool
	tag       string
	index     int    // 1-based, 0 means unset
	attrKey   string // optional [@attrKey='attrVal']
	attrVal   string
}

type xmlNode struct {
	name     string
	attrs    []xml.Attr
	text     strings.Builder
	children []*xmlNode
}

func (n *xmlNode) attr(name string) (string, bool) {
	for _, a := range n.attrs {
		if matchXMLName(a.Name.Local, name) || (a.Name.Space != "" && a.Name.Local == name) {
			return a.Value, true
		}
	}
	return "", false
}

func (n *xmlNode) appendAllText(b *strings.Builder) {
	b.WriteString(n.text.String())
	for _, c := range n.children {
		c.appendAllText(b)
	}
}

func matchXMLName(actual, expected string) bool {
	if expected == "*" || actual == expected {
		return true
	}
	if !strings.Contains(expected, ":") {
		if i := strings.LastIndexByte(actual, ':'); i >= 0 {
			return actual[i+1:] == expected
		}
	}
	return false
}

// CompileXPath parses and validates an XPath expression.
func CompileXPath(expr string) (*XPath, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("xpath 表达式不能为空")
	}
	xp := &XPath{src: raw}
	if strings.HasPrefix(raw, "@") {
		attr := strings.TrimSpace(raw[1:])
		if attr == "" {
			return nil, fmt.Errorf("xpath %q 属性名不能为空", expr)
		}
		xp.attr = attr
		return xp, nil
	}
	if !strings.HasPrefix(raw, "/") {
		return nil, fmt.Errorf("xpath %q 必须以 /、// 或 @ 开头", expr)
	}

	cur := raw
	for cur != "" {
		recursive := false
		if strings.HasPrefix(cur, "//") {
			recursive = true
			cur = cur[2:]
		} else if strings.HasPrefix(cur, "/") {
			cur = cur[1:]
		}
		if cur == "" {
			return nil, fmt.Errorf("xpath %q 不能以斜杠结尾", expr)
		}
		seg, rest := splitXPathSeg(cur)
		cur = rest

		if seg == "text()" {
			if cur != "" {
				return nil, fmt.Errorf("xpath %q 中 text() 只能出现在末尾", expr)
			}
			break
		}
		if strings.HasPrefix(seg, "@") {
			if cur != "" {
				return nil, fmt.Errorf("xpath %q 中 @属性 只能出现在末尾", expr)
			}
			attr := strings.TrimSpace(seg[1:])
			if attr == "" {
				return nil, fmt.Errorf("xpath %q 属性名不能为空", expr)
			}
			xp.attr = attr
			break
		}

		st, err := parseXPathStep(expr, seg, recursive)
		if err != nil {
			return nil, err
		}
		xp.steps = append(xp.steps, st)
	}

	if len(xp.steps) == 0 && xp.attr == "" {
		return nil, fmt.Errorf("xpath %q 缺少元素路径", expr)
	}
	return xp, nil
}

func splitXPathSeg(s string) (seg, rest string) {
	inQuote := byte(0)
	bracket := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuote = c
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '/':
			if bracket == 0 {
				return s[:i], s[i:]
			}
		}
	}
	return s, ""
}

func parseXPathStep(expr, seg string, recursive bool) (xpathStep, error) {
	st := xpathStep{recursive: recursive}
	bIdx := strings.IndexByte(seg, '[')
	if bIdx < 0 {
		if seg == "" {
			return st, fmt.Errorf("xpath %q 存在空路径段", expr)
		}
		st.tag = seg
		return st, nil
	}
	if !strings.HasSuffix(seg, "]") {
		return st, fmt.Errorf("xpath %q 谓词方括号未闭合", expr)
	}
	st.tag = strings.TrimSpace(seg[:bIdx])
	if st.tag == "" {
		return st, fmt.Errorf("xpath %q 谓词缺少标签名", expr)
	}
	pred := strings.TrimSpace(seg[bIdx+1 : len(seg)-1])
	if pred == "" {
		return st, fmt.Errorf("xpath %q 谓词不能为空", expr)
	}
	if idx, err := strconv.Atoi(pred); err == nil {
		if idx < 1 {
			return st, fmt.Errorf("xpath %q 索引谓词必须从 1 开始", expr)
		}
		st.index = idx
		return st, nil
	}
	if strings.HasPrefix(pred, "@") {
		eq := strings.IndexByte(pred, '=')
		if eq > 1 {
			k := strings.TrimSpace(pred[1:eq])
			v := strings.TrimSpace(pred[eq+1:])
			v = stripMatchingQuotes(v)
			if k != "" {
				st.attrKey = k
				st.attrVal = v
				return st, nil
			}
		}
	}
	return st, fmt.Errorf("xpath %q 不支持的谓词 [%s]", expr, pred)
}

func stripMatchingQuotes(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

// Eval evaluates the compiled XPath against an XML or HTML document body.
func (x *XPath) Eval(body []byte) (string, bool) {
	if x == nil || len(bytes.TrimSpace(body)) == 0 {
		return "", false
	}
	root := parseXMLTree(body)
	if len(root.children) == 0 {
		return "", false
	}

	var cur []*xmlNode
	if len(x.steps) == 0 && x.attr != "" {
		cur = root.children
	} else {
		cur = []*xmlNode{root}
		for _, st := range x.steps {
			cur = evalXPathStep(cur, st)
			if len(cur) == 0 {
				return "", false
			}
		}
	}

	first := cur[0]
	if x.attr != "" {
		return first.attr(x.attr)
	}
	var out strings.Builder
	first.appendAllText(&out)
	return out.String(), true
}

func evalXPathStep(cur []*xmlNode, st xpathStep) []*xmlNode {
	var next []*xmlNode
	for _, parent := range cur {
		if !st.recursive {
			matches := matchPredicateFilter(parent.children, st)
			next = append(next, matches...)
		} else {
			var all []*xmlNode
			collectDescendantMatches(parent, st.tag, &all)
			matches := matchAttrFilter(all, st)
			if st.index > 0 {
				if st.index <= len(matches) {
					next = append(next, matches[st.index-1])
				}
			} else {
				next = append(next, matches...)
			}
		}
	}
	return next
}

func matchPredicateFilter(nodes []*xmlNode, st xpathStep) []*xmlNode {
	var matched []*xmlNode
	for _, n := range nodes {
		if matchXMLName(n.name, st.tag) && stepAttrMatches(n, st) {
			matched = append(matched, n)
		}
	}
	if st.index > 0 {
		if st.index <= len(matched) {
			return []*xmlNode{matched[st.index-1]}
		}
		return nil
	}
	return matched
}

func matchAttrFilter(nodes []*xmlNode, st xpathStep) []*xmlNode {
	if st.attrKey == "" {
		return nodes
	}
	var out []*xmlNode
	for _, n := range nodes {
		if stepAttrMatches(n, st) {
			out = append(out, n)
		}
	}
	return out
}

func stepAttrMatches(n *xmlNode, st xpathStep) bool {
	if st.attrKey == "" {
		return true
	}
	v, ok := n.attr(st.attrKey)
	return ok && v == st.attrVal
}

func collectDescendantMatches(n *xmlNode, tag string, out *[]*xmlNode) {
	for _, ch := range n.children {
		if matchXMLName(ch.name, tag) {
			*out = append(*out, ch)
		}
		collectDescendantMatches(ch, tag, out)
	}
}

func parseXMLTree(body []byte) *xmlNode {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	root := &xmlNode{}
	stack := []*xmlNode{root}
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// Return partial parsed tree if trailing input is truncated.
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if t.Name.Space != "" && !strings.Contains(t.Name.Space, "://") {
				name = t.Name.Space + ":" + t.Name.Local
			}
			node := &xmlNode{name: name, attrs: t.Attr}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 1 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
	return root
}
