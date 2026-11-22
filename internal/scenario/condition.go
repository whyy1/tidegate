package scenario

import (
	"fmt"
	"strconv"
	"strings"
)

// CondOp identifies a pre-compiled condition comparison operator.
type CondOp uint8

const (
	CondEmpty CondOp = iota
	CondNotEmpty
	CondEq
	CondNeq
	CondContains
	CondNotContains
	CondGT
	CondLT
	CondGTE
	CondLTE
)

// Condition is a pre-compiled predicate used by If and Loop.While nodes.
type Condition struct {
	Src string
	LHS *Template
	RHS *Template
	Op  CondOp
}

// Eval evaluates the condition using pre-allocated stack buffers on the hot path.
func (c *Condition) Eval(env *Env) bool {
	if c == nil || c.LHS == nil {
		return false
	}
	var lbuf [128]byte
	lhs := string(c.LHS.Append(lbuf[:0], env))
	switch c.Op {
	case CondEmpty:
		return strings.TrimSpace(lhs) == ""
	case CondNotEmpty:
		return strings.TrimSpace(lhs) != ""
	}
	var rbuf [128]byte
	var rhs string
	if c.RHS != nil {
		rhs = string(c.RHS.Append(rbuf[:0], env))
	}
	switch c.Op {
	case CondEq:
		return lhs == rhs
	case CondNeq:
		return lhs != rhs
	case CondContains:
		return strings.Contains(lhs, rhs)
	case CondNotContains:
		return !strings.Contains(lhs, rhs)
	case CondGT, CondLT, CondGTE, CondLTE:
		lf, err1 := strconv.ParseFloat(strings.TrimSpace(lhs), 64)
		rf, err2 := strconv.ParseFloat(strings.TrimSpace(rhs), 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch c.Op {
		case CondGT:
			return lf > rf
		case CondLT:
			return lf < rf
		case CondGTE:
			return lf >= rf
		case CondLTE:
			return lf <= rf
		}
	}
	return false
}

func (c *compiler) compileCondition(where, expr string) *Condition {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		c.fail(where, "条件表达式不能为空")
		return nil
	}
	toks, err := splitConditionTokens(raw)
	if err != nil {
		c.fail(where, "%v", err)
		return nil
	}
	if len(toks) == 2 {
		opTok := strings.ToLower(toks[1].raw)
		var op CondOp
		switch opTok {
		case "empty":
			op = CondEmpty
		case "not_empty":
			op = CondNotEmpty
		default:
			c.fail(where, "未知的二元条件运算符 %q（支持 empty、not_empty）", toks[1].raw)
			return nil
		}
		return &Condition{
			Src: raw,
			LHS: c.template(where, toks[0].val, escapeNone),
			Op:  op,
		}
	}
	if len(toks) == 3 {
		opTok := strings.ToLower(toks[1].raw)
		var op CondOp
		switch opTok {
		case "==":
			op = CondEq
		case "!=":
			op = CondNeq
		case "contains":
			op = CondContains
		case "not_contains":
			op = CondNotContains
		case ">":
			op = CondGT
		case "<":
			op = CondLT
		case ">=":
			op = CondGTE
		case "<=":
			op = CondLTE
		default:
			c.fail(where, "未知的比较运算符 %q（支持 ==、!=、contains、not_contains、>、<、>=、<=）", toks[1].raw)
			return nil
		}
		return &Condition{
			Src: raw,
			LHS: c.template(where, toks[0].val, escapeNone),
			RHS: c.template(where, toks[2].val, escapeNone),
			Op:  op,
		}
	}
	c.fail(where, "条件表达式格式无效：%q（示例：`{{var}} == \"ok\"` 或 `{{token}} not_empty`）", raw)
	return nil
}

type condToken struct {
	raw string
	val string
}

func splitConditionTokens(s string) ([]condToken, error) {
	var out []condToken
	i := 0
	n := len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
		if i >= n {
			break
		}
		if s[i] == '"' || s[i] == '\'' {
			q := s[i]
			start := i
			i++
			var b strings.Builder
			closed := false
			for i < n {
				if s[i] == '\\' && i+1 < n {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == q {
					i++
					closed = true
					break
				}
				b.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("条件表达式引号未闭合")
			}
			out = append(out, condToken{raw: s[start:i], val: b.String()})
			continue
		}
		start := i
		braceDepth := 0
		for i < n {
			if i+1 < n && s[i] == '{' && s[i+1] == '{' {
				braceDepth++
				i += 2
				continue
			}
			if i+1 < n && s[i] == '}' && s[i+1] == '}' && braceDepth > 0 {
				braceDepth--
				i += 2
				continue
			}
			if braceDepth == 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
				break
			}
			i++
		}
		tok := s[start:i]
		out = append(out, condToken{raw: tok, val: tok})
	}
	return out, nil
}
