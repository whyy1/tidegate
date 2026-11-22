package scenario

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxScriptBytes caps script source length (64 KiB).
	MaxScriptBytes = 64 << 10
	// MaxScriptStatements caps total compiled AST statements per script.
	MaxScriptStatements = 512
	// MaxScriptOps caps per-execution VM/AST operations so scripts always terminate quickly.
	MaxScriptOps = 2000
	// MaxScriptStringBytes caps the byte length of any computed script string value.
	MaxScriptStringBytes = 256 << 10
)

// ErrScriptAssert indicates an assertion inside a script step failed.
var ErrScriptAssert = errors.New("脚本断言失败")

// ScriptCompileOptions connects script variable/dataset references to scenario compilation.
type ScriptCompileOptions struct {
	ResolveSlot  func(name string) (slot int, ok bool)
	ResolveConst func(name string) (tmpl *Template, ok bool)
	ResolveData  func(alias, col string) (dataIdx, colIdx int, err error)
}

// ScriptAssertOutcome records one assert() evaluation inside a script step.
type ScriptAssertOutcome struct {
	Passed  bool
	Message string
}

// ScriptOutcome holds debug/runtime outcomes from executing a compiled script.
type ScriptOutcome struct {
	Asserts     []ScriptAssertOutcome
	WrittenVars map[string]string
}

// ScriptProgram is an immutable, pre-compiled sandboxed script.
type ScriptProgram struct {
	Source        string
	OwnVars       []string // populated when compiled standalone without CompileOptions
	AssignedNames []string
	AssignedSlots []int
	stmts         []scriptStmt
	localsCount   int
}

// ExtractAssignedVars statically scans code and returns every variable name
// written via `vars.x = ...`, `vars["x"] = ...`, `env.x = ...`, or `set("x", ...)`.
func ExtractAssignedVars(code string) ([]string, error) {
	if len(code) > MaxScriptBytes {
		return nil, fmt.Errorf("脚本文本长度 %d 字节超过上限 %d 字节", len(code), MaxScriptBytes)
	}
	p, err := newScriptParser(code, nil, true)
	if err != nil {
		return nil, err
	}
	if _, _, err := p.parseProgram(); err != nil {
		return nil, err
	}
	return p.assignedOrder, nil
}

// CompileScript parses, validates, and resolves code into an executable ScriptProgram.
func CompileScript(code string, opts *ScriptCompileOptions) (*ScriptProgram, error) {
	if len(code) > MaxScriptBytes {
		return nil, fmt.Errorf("脚本文本长度 %d 字节超过上限 %d 字节", len(code), MaxScriptBytes)
	}
	p, err := newScriptParser(code, opts, false)
	if err != nil {
		return nil, err
	}
	stmts, _, err := p.parseProgram()
	if err != nil {
		return nil, err
	}
	prog := &ScriptProgram{
		Source:        code,
		OwnVars:       p.ownVars,
		AssignedNames: p.assignedOrder,
		AssignedSlots: make([]int, len(p.assignedOrder)),
		stmts:         stmts,
		localsCount:   len(p.locals),
	}
	for i, name := range p.assignedOrder {
		prog.AssignedSlots[i] = p.assignedSlots[name]
	}
	return prog, nil
}

// Exec executes the script against env, short-circuiting on the first runtime or
// assert error (used on load-generation hot paths).
func (p *ScriptProgram) Exec(env *Env) error {
	_, err := p.exec(env, true)
	return err
}

// ExecDebug executes the script and captures all assert outcomes and written
// variable values without short-circuiting assertions.
func (p *ScriptProgram) ExecDebug(env *Env) (*ScriptOutcome, error) {
	return p.exec(env, false)
}

func (p *ScriptProgram) exec(env *Env, stopOnAssertFail bool) (*ScriptOutcome, error) {
	var localEnv Env
	if env == nil {
		localEnv = Env{Vars: make([][]byte, len(p.OwnVars))}
		env = &localEnv
	}
	r := scriptRunner{
		prog:             p,
		env:              env,
		locals:           make([]scriptVal, p.localsCount),
		stopOnAssertFail: stopOnAssertFail,
		rng:              env.Rand,
	}
	if !stopOnAssertFail {
		r.written = make(map[string]string, len(p.AssignedNames))
	}
	err := r.execBlock(p.stmts)
	out := &ScriptOutcome{
		Asserts:     r.asserts,
		WrittenVars: r.written,
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Values

type scriptValKind uint8

const (
	valNull scriptValKind = iota
	valBool
	valInt
	valFloat
	valString
)

type scriptVal struct {
	kind scriptValKind
	b    bool
	i    int64
	f    float64
	s    string
}

func nullVal() scriptVal          { return scriptVal{kind: valNull} }
func boolVal(b bool) scriptVal    { return scriptVal{kind: valBool, b: b} }
func intVal(i int64) scriptVal    { return scriptVal{kind: valInt, i: i} }
func floatVal(f float64) scriptVal { return scriptVal{kind: valFloat, f: f} }
func strVal(s string) (scriptVal, error) {
	if len(s) > MaxScriptStringBytes {
		return scriptVal{}, fmt.Errorf("脚本字符串长度超出 %d 字节限制", MaxScriptStringBytes)
	}
	return scriptVal{kind: valString, s: s}, nil
}

func (v scriptVal) truthy() bool {
	switch v.kind {
	case valBool:
		return v.b
	case valInt:
		return v.i != 0
	case valFloat:
		return v.f != 0 && !math.IsNaN(v.f)
	case valString:
		return v.s != "" && v.s != "false" && v.s != "0"
	default:
		return false
	}
}

func (v scriptVal) asString() string {
	switch v.kind {
	case valNull:
		return ""
	case valBool:
		if v.b {
			return "true"
		}
		return "false"
	case valInt:
		return strconv.FormatInt(v.i, 10)
	case valFloat:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case valString:
		return v.s
	default:
		return ""
	}
}

func (v scriptVal) asNumber() (isFloat bool, i int64, f float64, err error) {
	switch v.kind {
	case valInt:
		return false, v.i, float64(v.i), nil
	case valFloat:
		return true, int64(v.f), v.f, nil
	case valBool:
		if v.b {
			return false, 1, 1, nil
		}
		return false, 0, 0, nil
	case valString:
		s := strings.TrimSpace(v.s)
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil {
			return false, n, float64(n), nil
		}
		if fl, perr := strconv.ParseFloat(s, 64); perr == nil && !math.IsNaN(fl) && !math.IsInf(fl, 0) {
			return true, int64(fl), fl, nil
		}
		return false, 0, 0, fmt.Errorf("值 %q 无法转换为数字", v.s)
	default:
		return false, 0, 0, errors.New("null 无法转换为数字")
	}
}

// ---------------------------------------------------------------------------
// Lexer

type sTokKind uint8

const (
	sTokEOF sTokKind = iota
	sTokIdent
	sTokString
	sTokInt
	sTokFloat
	sTokSemi
	sTokDot
	sTokComma
	sTokLParen
	sTokRParen
	sTokLBrace
	sTokRBrace
	sTokLBrack
	sTokRBrack
	sTokQuestion
	sTokColon
	sTokAssign
	sTokEq
	sTokNeq
	sTokLT
	sTokLTE
	sTokGT
	sTokGTE
	sTokAnd
	sTokOr
	sTokNot
	sTokPlus
	sTokMinus
	sTokMul
	sTokDiv
	sTokMod
)

type sTok struct {
	kind sTokKind
	val  string
	line int
}

func tokenizeScript(src string) ([]sTok, error) {
	var toks []sTok
	line := 1
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if c == '\n' {
			line++
			i++
			continue
		}
		// Line comments: // or #
		if (c == '/' && i+1 < n && src[i+1] == '/') || c == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Block comments: /* ... */
		if c == '/' && i+1 < n && src[i+1] == '*' {
			startLine := line
			i += 2
			closed := false
			for i+1 < n {
				if src[i] == '\n' {
					line++
				}
				if src[i] == '*' && src[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("脚本第 %d 行：块注释未闭合", startLine)
			}
			continue
		}
		// String literal
		if c == '"' || c == '\'' {
			q := c
			startLine := line
			i++
			var b strings.Builder
			closed := false
			for i < n {
				ch := src[i]
				if ch == '\n' {
					return nil, fmt.Errorf("脚本第 %d 行：字符串字面量不能跨行未闭合", startLine)
				}
				if ch == '\\' && i+1 < n {
					i++
					switch src[i] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case '\\', '"', '\'':
						b.WriteByte(src[i])
					default:
						b.WriteByte(src[i])
					}
					i++
					continue
				}
				if ch == q {
					i++
					closed = true
					break
				}
				b.WriteByte(ch)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("脚本第 %d 行：字符串引号未闭合", startLine)
			}
			toks = append(toks, sTok{kind: sTokString, val: b.String(), line: startLine})
			continue
		}
		// Number
		if c >= '0' && c <= '9' {
			start := i
			isFloat := false
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i+1 < n && src[i] == '.' && src[i+1] >= '0' && src[i+1] <= '9' {
				isFloat = true
				i++
				for i < n && src[i] >= '0' && src[i] <= '9' {
					i++
				}
			}
			if i < n && (src[i] == 'e' || src[i] == 'E') {
				isFloat = true
				i++
				if i < n && (src[i] == '+' || src[i] == '-') {
					i++
				}
				if i >= n || src[i] < '0' || src[i] > '9' {
					return nil, fmt.Errorf("脚本第 %d 行：数字指数格式无效", line)
				}
				for i < n && src[i] >= '0' && src[i] <= '9' {
					i++
				}
			}
			kind := sTokInt
			if isFloat {
				kind = sTokFloat
			}
			toks = append(toks, sTok{kind: kind, val: src[start:i], line: line})
			continue
		}
		// Identifier / Keyword
		if isScriptIdentStart(c) {
			start := i
			i++
			for i < n && isScriptIdentContinue(src[i]) {
				i++
			}
			toks = append(toks, sTok{kind: sTokIdent, val: src[start:i], line: line})
			continue
		}
		// Two-char operators
		if i+1 < n {
			two := src[i : i+2]
			var k sTokKind
			switch two {
			case "==":
				k = sTokEq
			case "!=":
				k = sTokNeq
			case "<=":
				k = sTokLTE
			case ">=":
				k = sTokGTE
			case "&&":
				k = sTokAnd
			case "||":
				k = sTokOr
			}
			if k != 0 {
				toks = append(toks, sTok{kind: k, val: two, line: line})
				i += 2
				continue
			}
		}
		// Single-char tokens
		var k sTokKind
		switch c {
		case ';':
			k = sTokSemi
		case '.':
			k = sTokDot
		case ',':
			k = sTokComma
		case '(':
			k = sTokLParen
		case ')':
			k = sTokRParen
		case '{':
			k = sTokLBrace
		case '}':
			k = sTokRBrace
		case '[':
			k = sTokLBrack
		case ']':
			k = sTokRBrack
		case '?':
			k = sTokQuestion
		case ':':
			k = sTokColon
		case '=':
			k = sTokAssign
		case '<':
			k = sTokLT
		case '>':
			k = sTokGT
		case '!':
			k = sTokNot
		case '+':
			k = sTokPlus
		case '-':
			k = sTokMinus
		case '*':
			k = sTokMul
		case '/':
			k = sTokDiv
		case '%':
			k = sTokMod
		default:
			return nil, fmt.Errorf("脚本第 %d 行：无法识别的字符 %q", line, c)
		}
		toks = append(toks, sTok{kind: k, val: src[i : i+1], line: line})
		i++
	}
	toks = append(toks, sTok{kind: sTokEOF, line: line})
	return toks, nil
}

func isScriptIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isScriptIdentContinue(c byte) bool {
	return isScriptIdentStart(c) || (c >= '0' && c <= '9')
}

// ---------------------------------------------------------------------------
// AST nodes

type scriptStmt interface {
	isScriptStmt()
}

type stmtBlockIf struct {
	cond scriptExpr
	then []scriptStmt
	els  []scriptStmt
}

type stmtAssignLocal struct {
	localIdx int
	val      scriptExpr
}

type stmtAssignVar struct {
	name string
	slot int
	val  scriptExpr
}

type stmtExpr struct {
	expr scriptExpr
}

func (*stmtBlockIf) isScriptStmt()     {}
func (*stmtAssignLocal) isScriptStmt() {}
func (*stmtAssignVar) isScriptStmt()   {}
func (*stmtExpr) isScriptStmt()        {}

type scriptExpr interface {
	isScriptExpr()
}

type exprLit struct{ val scriptVal }
type exprLocal struct{ idx int }
type exprSlotVar struct{ slot int }
type exprConstTmpl struct{ tmpl *Template }
type exprDataCol struct {
	dataIdx int
	colIdx  int
	alias   string
	col     string
}
type exprUnary struct {
	op   sTokKind
	rhs  scriptExpr
	line int
}
type exprBinary struct {
	op   sTokKind
	lhs  scriptExpr
	rhs  scriptExpr
	line int
}
type exprTernary struct {
	cond scriptExpr
	yes  scriptExpr
	no   scriptExpr
}
type exprCall struct {
	name     string
	args     []scriptExpr
	jsonPath *JSONPath // pre-compiled for static json_get 2nd arg
	line     int
}
type exprSetVar struct {
	name string
	slot int
	val  scriptExpr
}

func (*exprLit) isScriptExpr()       {}
func (*exprLocal) isScriptExpr()     {}
func (*exprSlotVar) isScriptExpr()   {}
func (*exprConstTmpl) isScriptExpr() {}
func (*exprDataCol) isScriptExpr()   {}
func (*exprUnary) isScriptExpr()     {}
func (*exprBinary) isScriptExpr()    {}
func (*exprTernary) isScriptExpr()   {}
func (*exprCall) isScriptExpr()      {}
func (*exprSetVar) isScriptExpr()    {}

// ---------------------------------------------------------------------------
// Parser

type scriptParser struct {
	toks          []sTok
	pos           int
	lastLine      int
	opts          *ScriptCompileOptions
	discoverOnly  bool
	stmtCount     int
	locals        map[string]int
	assignedSeen  map[string]bool
	assignedOrder []string
	assignedSlots map[string]int
	ownVars       []string
	ownSlots      map[string]int
}

func newScriptParser(code string, opts *ScriptCompileOptions, discoverOnly bool) (*scriptParser, error) {
	toks, err := tokenizeScript(code)
	if err != nil {
		return nil, err
	}
	return &scriptParser{
		toks:          toks,
		opts:          opts,
		discoverOnly:  discoverOnly,
		locals:        make(map[string]int),
		assignedSeen:  make(map[string]bool),
		assignedSlots: make(map[string]int),
		ownSlots:      make(map[string]int),
	}, nil
}

func (p *scriptParser) peek() sTok { return p.toks[p.pos] }
func (p *scriptParser) peekAt(off int) sTok {
	if p.pos+off < len(p.toks) {
		return p.toks[p.pos+off]
	}
	return p.toks[len(p.toks)-1]
}
func (p *scriptParser) next() sTok {
	t := p.toks[p.pos]
	if t.kind != sTokEOF {
		p.lastLine = t.line
		p.pos++
	}
	return t
}

func (p *scriptParser) recordAssigned(name string, line int) (int, error) {
	if !identRe.MatchString(name) {
		return 0, fmt.Errorf("脚本第 %d 行：变量名 %q 无效", line, name)
	}
	if !p.assignedSeen[name] {
		p.assignedSeen[name] = true
		p.assignedOrder = append(p.assignedOrder, name)
	}
	if p.discoverOnly {
		return 0, nil
	}
	if slot, ok := p.assignedSlots[name]; ok {
		return slot, nil
	}
	var slot int
	if p.opts != nil && p.opts.ResolveSlot != nil {
		s, ok := p.opts.ResolveSlot(name)
		if !ok {
			return 0, fmt.Errorf("脚本第 %d 行：未找到变量 %q 的存储槽", line, name)
		}
		slot = s
	} else {
		slot = len(p.ownVars)
		p.ownVars = append(p.ownVars, name)
		p.ownSlots[name] = slot
	}
	p.assignedSlots[name] = slot
	return slot, nil
}

func (p *scriptParser) localSlot(name string) int {
	if idx, ok := p.locals[name]; ok {
		return idx
	}
	idx := len(p.locals)
	p.locals[name] = idx
	return idx
}

func (p *scriptParser) parseProgram() ([]scriptStmt, int, error) {
	stmts, err := p.parseStmtList(false)
	if err != nil {
		return nil, 0, err
	}
	return stmts, len(p.locals), nil
}

func (p *scriptParser) parseStmtList(untilBrace bool) ([]scriptStmt, error) {
	var out []scriptStmt
	for {
		for p.peek().kind == sTokSemi {
			p.next()
		}
		t := p.peek()
		if t.kind == sTokEOF {
			if untilBrace {
				return nil, fmt.Errorf("脚本第 %d 行：缺少闭合的 }", t.line)
			}
			return out, nil
		}
		if untilBrace && t.kind == sTokRBrace {
			return out, nil
		}
		st, needSep, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		out = append(out, st)
		p.stmtCount++
		if p.stmtCount > MaxScriptStatements {
			return nil, fmt.Errorf("脚本语句数量超过上限 %d", MaxScriptStatements)
		}
		if needSep {
			nt := p.peek()
			switch {
			case nt.kind == sTokSemi:
				p.next()
			case nt.kind == sTokEOF, nt.kind == sTokRBrace:
			case nt.line > p.lastLine:
			default:
				return nil, fmt.Errorf("脚本第 %d 行：语句末尾需要换行或分号（意外的 %q）", nt.line, nt.val)
			}
		}
	}
}

func (p *scriptParser) parseStmt() (scriptStmt, bool, error) {
	t := p.peek()
	// if (<expr>) { ... } [else { ... }]
	if t.kind == sTokIdent && t.val == "if" {
		p.next()
		if p.peek().kind != sTokLParen {
			return nil, false, fmt.Errorf("脚本第 %d 行：if 后需要 (", t.line)
		}
		p.next()
		cond, err := p.parseExpr()
		if err != nil {
			return nil, false, err
		}
		if p.peek().kind != sTokRParen {
			return nil, false, fmt.Errorf("脚本第 %d 行：if 条件缺少 )", p.peek().line)
		}
		p.next()
		if p.peek().kind != sTokLBrace {
			return nil, false, fmt.Errorf("脚本第 %d 行：if 条件后需要 {", p.peek().line)
		}
		p.next()
		thenStmts, err := p.parseStmtList(true)
		if err != nil {
			return nil, false, err
		}
		if p.peek().kind != sTokRBrace {
			return nil, false, fmt.Errorf("脚本第 %d 行：if 语句块缺少 }", p.peek().line)
		}
		p.next()
		var elseStmts []scriptStmt
		if p.peek().kind == sTokIdent && p.peek().val == "else" {
			p.next()
			if p.peek().kind == sTokIdent && p.peek().val == "if" {
				nested, _, err := p.parseStmt()
				if err != nil {
					return nil, false, err
				}
				elseStmts = []scriptStmt{nested}
			} else {
				if p.peek().kind != sTokLBrace {
					return nil, false, fmt.Errorf("脚本第 %d 行：else 后需要 {", p.peek().line)
				}
				p.next()
				elseStmts, err = p.parseStmtList(true)
				if err != nil {
					return nil, false, err
				}
				if p.peek().kind != sTokRBrace {
					return nil, false, fmt.Errorf("脚本第 %d 行：else 语句块缺少 }", p.peek().line)
				}
				p.next()
			}
		}
		return &stmtBlockIf{cond: cond, then: thenStmts, els: elseStmts}, false, nil
	}

	// let <ident> = <expr>
	if t.kind == sTokIdent && t.val == "let" {
		p.next()
		nameTok := p.peek()
		if nameTok.kind != sTokIdent {
			return nil, false, fmt.Errorf("脚本第 %d 行：let 后需要变量名", t.line)
		}
		if nameTok.val == "vars" || nameTok.val == "env" || nameTok.val == "data" {
			return nil, false, fmt.Errorf("脚本第 %d 行：保留关键字 %q 不能作为局部变量名", nameTok.line, nameTok.val)
		}
		p.next()
		if p.peek().kind != sTokAssign {
			return nil, false, fmt.Errorf("脚本第 %d 行：局部变量 %q 需要初始化 =", nameTok.line, nameTok.val)
		}
		p.next()
		val, err := p.parseExpr()
		if err != nil {
			return nil, false, err
		}
		return &stmtAssignLocal{localIdx: p.localSlot(nameTok.val), val: val}, true, nil
	}

	// vars.<ident> = <expr>  OR  env.<ident> = <expr>
	if t.kind == sTokIdent && (t.val == "vars" || t.val == "env") && p.peekAt(1).kind == sTokDot && p.peekAt(2).kind == sTokIdent && p.peekAt(3).kind == sTokAssign {
		p.next() // vars/env
		p.next() // .
		nameTok := p.next()
		p.next() // =
		slot, err := p.recordAssigned(nameTok.val, nameTok.line)
		if err != nil {
			return nil, false, err
		}
		val, err := p.parseExpr()
		if err != nil {
			return nil, false, err
		}
		return &stmtAssignVar{name: nameTok.val, slot: slot, val: val}, true, nil
	}

	// vars["<ident>"] = <expr>  OR  env["<ident>"] = <expr>
	if t.kind == sTokIdent && (t.val == "vars" || t.val == "env") && p.peekAt(1).kind == sTokLBrack && p.peekAt(2).kind == sTokString && p.peekAt(3).kind == sTokRBrack && p.peekAt(4).kind == sTokAssign {
		p.next() // vars/env
		p.next() // [
		nameTok := p.next()
		p.next() // ]
		p.next() // =
		slot, err := p.recordAssigned(nameTok.val, nameTok.line)
		if err != nil {
			return nil, false, err
		}
		val, err := p.parseExpr()
		if err != nil {
			return nil, false, err
		}
		return &stmtAssignVar{name: nameTok.val, slot: slot, val: val}, true, nil
	}

	// <ident> = <expr> (local shorthand)
	if t.kind == sTokIdent && p.peekAt(1).kind == sTokAssign {
		if t.val == "vars" || t.val == "env" || t.val == "data" {
			return nil, false, fmt.Errorf("脚本第 %d 行：不能直接覆盖保留对象 %q", t.line, t.val)
		}
		p.next()
		p.next()
		val, err := p.parseExpr()
		if err != nil {
			return nil, false, err
		}
		return &stmtAssignLocal{localIdx: p.localSlot(t.val), val: val}, true, nil
	}

	// Expression statement (e.g. assert(...), set("x", ...))
	ex, err := p.parseExpr()
	if err != nil {
		return nil, false, err
	}
	return &stmtExpr{expr: ex}, true, nil
}

func (p *scriptParser) parseExpr() (scriptExpr, error) { return p.parseTernary() }

func (p *scriptParser) parseTernary() (scriptExpr, error) {
	cond, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == sTokQuestion {
		p.next()
		yes, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != sTokColon {
			return nil, fmt.Errorf("脚本第 %d 行：三目运算符缺少 :", p.peek().line)
		}
		p.next()
		no, err := p.parseTernary()
		if err != nil {
			return nil, err
		}
		return &exprTernary{cond: cond, yes: yes, no: no}, nil
	}
	return cond, nil
}

func (p *scriptParser) parseOr() (scriptExpr, error) {
	lhs, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == sTokOr {
		op := p.next()
		rhs, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseAnd() (scriptExpr, error) {
	lhs, err := p.parseEq()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == sTokAnd {
		op := p.next()
		rhs, err := p.parseEq()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseEq() (scriptExpr, error) {
	lhs, err := p.parseRel()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == sTokEq || p.peek().kind == sTokNeq {
		op := p.next()
		rhs, err := p.parseRel()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseRel() (scriptExpr, error) {
	lhs, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	for k := p.peek().kind; k == sTokLT || k == sTokLTE || k == sTokGT || k == sTokGTE; k = p.peek().kind {
		op := p.next()
		rhs, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseAdd() (scriptExpr, error) {
	lhs, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == sTokPlus || p.peek().kind == sTokMinus {
		op := p.next()
		rhs, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseMul() (scriptExpr, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for k := p.peek().kind; k == sTokMul || k == sTokDiv || k == sTokMod; k = p.peek().kind {
		op := p.next()
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		lhs = &exprBinary{op: op.kind, lhs: lhs, rhs: rhs, line: op.line}
	}
	return lhs, nil
}

func (p *scriptParser) parseUnary() (scriptExpr, error) {
	t := p.peek()
	if t.kind == sTokNot || t.kind == sTokMinus || t.kind == sTokPlus {
		p.next()
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &exprUnary{op: t.kind, rhs: rhs, line: t.line}, nil
	}
	return p.parsePrimary()
}

func (p *scriptParser) parsePrimary() (scriptExpr, error) {
	t := p.next()
	switch t.kind {
	case sTokString:
		v, err := strVal(t.val)
		if err != nil {
			return nil, fmt.Errorf("脚本第 %d 行：%v", t.line, err)
		}
		return &exprLit{val: v}, nil
	case sTokInt:
		n, err := strconv.ParseInt(t.val, 10, 64)
		if err != nil {
			f, ferr := strconv.ParseFloat(t.val, 64)
			if ferr != nil {
				return nil, fmt.Errorf("脚本第 %d 行：整数 %q 溢出", t.line, t.val)
			}
			return &exprLit{val: floatVal(f)}, nil
		}
		return &exprLit{val: intVal(n)}, nil
	case sTokFloat:
		f, err := strconv.ParseFloat(t.val, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("脚本第 %d 行：浮点数 %q 无效", t.line, t.val)
		}
		return &exprLit{val: floatVal(f)}, nil
	case sTokLParen:
		ex, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != sTokRParen {
			return nil, fmt.Errorf("脚本第 %d 行：括号未闭合，缺少 )", p.peek().line)
		}
		p.next()
		return ex, nil
	case sTokIdent:
		switch t.val {
		case "true":
			return &exprLit{val: boolVal(true)}, nil
		case "false":
			return &exprLit{val: boolVal(false)}, nil
		case "null":
			return &exprLit{val: nullVal()}, nil
		case "vars", "env":
			return p.parseVarRead(t)
		case "data":
			return p.parseDataRead(t)
		}
		if p.peek().kind == sTokLParen {
			return p.parseCall(t)
		}
		if idx, ok := p.locals[t.val]; ok {
			return &exprLocal{idx: idx}, nil
		}
		return nil, fmt.Errorf("脚本第 %d 行：未定义的标识符 %q（读写场景变量请使用 vars.%s）", t.line, t.val, t.val)
	default:
		return nil, fmt.Errorf("脚本第 %d 行：意外的标记 %q", t.line, t.val)
	}
}

func (p *scriptParser) parseVarRead(prefix sTok) (scriptExpr, error) {
	var name string
	var line int
	switch p.peek().kind {
	case sTokDot:
		p.next()
		id := p.peek()
		if id.kind != sTokIdent {
			return nil, fmt.Errorf("脚本第 %d 行：%s. 后需要变量名", id.line, prefix.val)
		}
		p.next()
		name, line = id.val, id.line
	case sTokLBrack:
		p.next()
		str := p.peek()
		if str.kind != sTokString {
			return nil, fmt.Errorf("脚本第 %d 行：%s[...] 仅支持常量字符串变量名", str.line, prefix.val)
		}
		p.next()
		if p.peek().kind != sTokRBrack {
			return nil, fmt.Errorf("脚本第 %d 行：%s[...] 缺少 ]", p.peek().line, prefix.val)
		}
		p.next()
		name, line = str.val, str.line
	default:
		return nil, fmt.Errorf("脚本第 %d 行：%s 后需要 .变量名 或 [\"变量名\"]", p.peek().line, prefix.val)
	}
	return p.resolveVarRead(name, line)
}

func (p *scriptParser) resolveVarRead(name string, line int) (scriptExpr, error) {
	if !identRe.MatchString(name) {
		return nil, fmt.Errorf("脚本第 %d 行：变量名 %q 无效", line, name)
	}
	if p.discoverOnly {
		return &exprLit{val: nullVal()}, nil
	}
	if p.opts == nil {
		if slot, ok := p.ownSlots[name]; ok {
			return &exprSlotVar{slot: slot}, nil
		}
		slot := len(p.ownVars)
		p.ownVars = append(p.ownVars, name)
		p.ownSlots[name] = slot
		return &exprSlotVar{slot: slot}, nil
	}
	if p.opts.ResolveSlot != nil {
		if slot, ok := p.opts.ResolveSlot(name); ok {
			return &exprSlotVar{slot: slot}, nil
		}
	}
	if p.opts.ResolveConst != nil {
		if tmpl, ok := p.opts.ResolveConst(name); ok {
			return &exprConstTmpl{tmpl: tmpl}, nil
		}
	}
	return nil, fmt.Errorf("脚本第 %d 行：引用了未定义变量 %q", line, name)
}

func (p *scriptParser) parseDataRead(prefix sTok) (scriptExpr, error) {
	if p.peek().kind != sTokDot {
		return nil, fmt.Errorf("脚本第 %d 行：data 后需要 .数据源名.列名", prefix.line)
	}
	p.next()
	aliasTok := p.peek()
	if aliasTok.kind != sTokIdent {
		return nil, fmt.Errorf("脚本第 %d 行：data. 后需要数据源名称", aliasTok.line)
	}
	p.next()
	if p.peek().kind != sTokDot {
		return nil, fmt.Errorf("脚本第 %d 行：data.%s 后需要 .列名", p.peek().line, aliasTok.val)
	}
	p.next()
	colTok := p.peek()
	if colTok.kind != sTokIdent && colTok.kind != sTokString {
		return nil, fmt.Errorf("脚本第 %d 行：data.%s. 后需要列名", colTok.line, aliasTok.val)
	}
	p.next()
	if p.discoverOnly || p.opts == nil || p.opts.ResolveData == nil {
		return &exprDataCol{dataIdx: -1, colIdx: -1, alias: aliasTok.val, col: colTok.val}, nil
	}
	di, ci, err := p.opts.ResolveData(aliasTok.val, colTok.val)
	if err != nil {
		return nil, fmt.Errorf("脚本第 %d 行：%v", colTok.line, err)
	}
	return &exprDataCol{dataIdx: di, colIdx: ci, alias: aliasTok.val, col: colTok.val}, nil
}

func (p *scriptParser) parseCall(fnTok sTok) (scriptExpr, error) {
	p.next() // consume '('
	var args []scriptExpr
	if p.peek().kind != sTokRParen {
		for {
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if p.peek().kind == sTokComma {
				p.next()
				continue
			}
			break
		}
	}
	if p.peek().kind != sTokRParen {
		return nil, fmt.Errorf("脚本第 %d 行：函数 %s(...) 缺少闭合括号 )", p.peek().line, fnTok.val)
	}
	p.next()

	switch fnTok.val {
	case "set":
		if len(args) != 2 {
			return nil, fmt.Errorf("脚本第 %d 行：set(name, val) 需要 2 个参数", fnTok.line)
		}
		lit, ok := args[0].(*exprLit)
		if !ok || lit.val.kind != valString {
			return nil, fmt.Errorf("脚本第 %d 行：set 的第 1 个参数必须是常量字符串变量名", fnTok.line)
		}
		slot, err := p.recordAssigned(lit.val.s, fnTok.line)
		if err != nil {
			return nil, err
		}
		return &exprSetVar{name: lit.val.s, slot: slot, val: args[1]}, nil
	case "get":
		if len(args) != 1 {
			return nil, fmt.Errorf("脚本第 %d 行：get(name) 需要 1 个参数", fnTok.line)
		}
		lit, ok := args[0].(*exprLit)
		if !ok || lit.val.kind != valString {
			return nil, fmt.Errorf("脚本第 %d 行：get 的参数必须是常量字符串变量名", fnTok.line)
		}
		return p.resolveVarRead(lit.val.s, fnTok.line)
	case "json_get":
		if len(args) != 2 {
			return nil, fmt.Errorf("脚本第 %d 行：json_get(jsonStr, path) 需要 2 个参数", fnTok.line)
		}
		var jp *JSONPath
		if lit, ok := args[1].(*exprLit); ok && lit.val.kind == valString {
			compiled, err := CompileJSONPath(lit.val.s)
			if err != nil {
				return nil, fmt.Errorf("脚本第 %d 行：json_get 路径无效：%v", fnTok.line, err)
			}
			jp = compiled
		}
		return &exprCall{name: fnTok.val, args: args, jsonPath: jp, line: fnTok.line}, nil
	case "hmac_sha256", "hmac_sha256_base64", "contains", "starts_with", "ends_with":
		if len(args) != 2 {
			return nil, fmt.Errorf("脚本第 %d 行：%s 需要 2 个参数", fnTok.line, fnTok.val)
		}
	case "sha256", "md5", "base64_encode", "base64_decode", "url_encode", "url_decode", "hex_encode",
		"len", "upper", "lower", "trim", "to_string", "to_number", "random_string":
		if len(args) != 1 {
			return nil, fmt.Errorf("脚本第 %d 行：%s 需要 1 个参数", fnTok.line, fnTok.val)
		}
	case "replace":
		if len(args) != 3 {
			return nil, fmt.Errorf("脚本第 %d 行：replace(s, old, new) 需要 3 个参数", fnTok.line)
		}
	case "substr":
		if len(args) != 2 && len(args) != 3 {
			return nil, fmt.Errorf("脚本第 %d 行：substr(s, start[, len]) 需要 2 或 3 个参数", fnTok.line)
		}
	case "now_ms", "now_sec", "uuid":
		if len(args) != 0 {
			return nil, fmt.Errorf("脚本第 %d 行：%s 不接受参数", fnTok.line, fnTok.val)
		}
	case "random_int":
		if len(args) != 2 {
			return nil, fmt.Errorf("脚本第 %d 行：random_int(min, max) 需要 2 个参数", fnTok.line)
		}
	case "assert":
		if len(args) != 1 && len(args) != 2 {
			return nil, fmt.Errorf("脚本第 %d 行：assert(cond[, message]) 需要 1 或 2 个参数", fnTok.line)
		}
	default:
		return nil, fmt.Errorf("脚本第 %d 行：不支持的函数 %q", fnTok.line, fnTok.val)
	}
	return &exprCall{name: fnTok.val, args: args, line: fnTok.line}, nil
}

// ---------------------------------------------------------------------------
// Evaluator

type scriptRunner struct {
	prog             *ScriptProgram
	env              *Env
	locals           []scriptVal
	ops              int
	stopOnAssertFail bool
	asserts          []ScriptAssertOutcome
	written          map[string]string
	rng              *rand.Rand
}

func (r *scriptRunner) step() error {
	r.ops++
	if r.ops > MaxScriptOps {
		return fmt.Errorf("脚本执行步骤数超过上限 %d", MaxScriptOps)
	}
	return nil
}

func (r *scriptRunner) randSource() *rand.Rand {
	if r.rng != nil {
		return r.rng
	}
	r.rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9E3779B97F4A7C15))
	return r.rng
}

func (r *scriptRunner) writeVar(name string, slot int, val string) {
	if slot >= 0 && slot < len(r.env.Vars) {
		r.env.Vars[slot] = append(r.env.Vars[slot][:0], val...)
	}
	if r.written != nil {
		r.written[name] = val
	}
}

func (r *scriptRunner) execBlock(stmts []scriptStmt) error {
	for _, st := range stmts {
		if err := r.step(); err != nil {
			return err
		}
		switch s := st.(type) {
		case *stmtBlockIf:
			v, err := r.eval(s.cond)
			if err != nil {
				return err
			}
			if v.truthy() {
				if err := r.execBlock(s.then); err != nil {
					return err
				}
			} else if len(s.els) > 0 {
				if err := r.execBlock(s.els); err != nil {
					return err
				}
			}
		case *stmtAssignLocal:
			v, err := r.eval(s.val)
			if err != nil {
				return err
			}
			r.locals[s.localIdx] = v
		case *stmtAssignVar:
			v, err := r.eval(s.val)
			if err != nil {
				return err
			}
			r.writeVar(s.name, s.slot, v.asString())
		case *stmtExpr:
			if _, err := r.eval(s.expr); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *scriptRunner) eval(ex scriptExpr) (scriptVal, error) {
	if err := r.step(); err != nil {
		return scriptVal{}, err
	}
	switch e := ex.(type) {
	case *exprLit:
		return e.val, nil
	case *exprLocal:
		return r.locals[e.idx], nil
	case *exprSlotVar:
		if e.slot >= 0 && e.slot < len(r.env.Vars) {
			return strVal(string(r.env.Vars[e.slot]))
		}
		return nullVal(), nil
	case *exprConstTmpl:
		return strVal(string(e.tmpl.Append(nil, r.env)))
	case *exprDataCol:
		if e.dataIdx >= 0 && e.dataIdx < len(r.env.Rows) {
			row := r.env.Rows[e.dataIdx]
			if e.colIdx >= 0 && e.colIdx < len(row) {
				return strVal(row[e.colIdx])
			}
		}
		return nullVal(), nil
	case *exprUnary:
		rv, err := r.eval(e.rhs)
		if err != nil {
			return scriptVal{}, err
		}
		switch e.op {
		case sTokNot:
			return boolVal(!rv.truthy()), nil
		case sTokPlus:
			isF, iv, fv, err := rv.asNumber()
			if err != nil {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", e.line, err)
			}
			if isF {
				return floatVal(fv), nil
			}
			return intVal(iv), nil
		case sTokMinus:
			isF, iv, fv, err := rv.asNumber()
			if err != nil {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", e.line, err)
			}
			if isF {
				return floatVal(-fv), nil
			}
			return intVal(-iv), nil
		}
	case *exprTernary:
		cv, err := r.eval(e.cond)
		if err != nil {
			return scriptVal{}, err
		}
		if cv.truthy() {
			return r.eval(e.yes)
		}
		return r.eval(e.no)
	case *exprBinary:
		return r.evalBinary(e)
	case *exprSetVar:
		v, err := r.eval(e.val)
		if err != nil {
			return scriptVal{}, err
		}
		s := v.asString()
		r.writeVar(e.name, e.slot, s)
		return strVal(s)
	case *exprCall:
		return r.evalCall(e)
	}
	return nullVal(), nil
}

func (r *scriptRunner) evalBinary(e *exprBinary) (scriptVal, error) {
	lv, err := r.eval(e.lhs)
	if err != nil {
		return scriptVal{}, err
	}
	switch e.op {
	case sTokOr:
		if lv.truthy() {
			return boolVal(true), nil
		}
		rv, err := r.eval(e.rhs)
		if err != nil {
			return scriptVal{}, err
		}
		return boolVal(rv.truthy()), nil
	case sTokAnd:
		if !lv.truthy() {
			return boolVal(false), nil
		}
		rv, err := r.eval(e.rhs)
		if err != nil {
			return scriptVal{}, err
		}
		return boolVal(rv.truthy()), nil
	}
	rv, err := r.eval(e.rhs)
	if err != nil {
		return scriptVal{}, err
	}
	switch e.op {
	case sTokEq:
		return boolVal(valsEqual(lv, rv)), nil
	case sTokNeq:
		return boolVal(!valsEqual(lv, rv)), nil
	case sTokLT, sTokLTE, sTokGT, sTokGTE:
		return r.evalCompare(e.op, lv, rv, e.line)
	case sTokPlus:
		if isNumericKind(lv.kind) && isNumericKind(rv.kind) {
			return evalNumArith(sTokPlus, lv, rv, e.line)
		}
		return strVal(lv.asString() + rv.asString())
	case sTokMinus, sTokMul, sTokDiv, sTokMod:
		return evalNumArith(e.op, lv, rv, e.line)
	}
	return nullVal(), nil
}

func isNumericKind(k scriptValKind) bool {
	return k == valInt || k == valFloat || k == valBool
}

func valsEqual(a, b scriptVal) bool {
	if a.kind == valNull && b.kind == valNull {
		return true
	}
	if a.kind == valNull || b.kind == valNull {
		return false
	}
	if isNumericKind(a.kind) && isNumericKind(b.kind) {
		isF1, i1, f1, _ := a.asNumber()
		isF2, i2, f2, _ := b.asNumber()
		if !isF1 && !isF2 {
			return i1 == i2
		}
		return f1 == f2
	}
	return a.asString() == b.asString()
}

func (r *scriptRunner) evalCompare(op sTokKind, a, b scriptVal, line int) (scriptVal, error) {
	if a.kind == valString && b.kind == valString {
		switch op {
		case sTokLT:
			return boolVal(a.s < b.s), nil
		case sTokLTE:
			return boolVal(a.s <= b.s), nil
		case sTokGT:
			return boolVal(a.s > b.s), nil
		case sTokGTE:
			return boolVal(a.s >= b.s), nil
		}
	}
	isF1, i1, f1, err := a.asNumber()
	if err != nil {
		return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", line, err)
	}
	isF2, i2, f2, err := b.asNumber()
	if err != nil {
		return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", line, err)
	}
	if !isF1 && !isF2 {
		switch op {
		case sTokLT:
			return boolVal(i1 < i2), nil
		case sTokLTE:
			return boolVal(i1 <= i2), nil
		case sTokGT:
			return boolVal(i1 > i2), nil
		case sTokGTE:
			return boolVal(i1 >= i2), nil
		}
	}
	switch op {
	case sTokLT:
		return boolVal(f1 < f2), nil
	case sTokLTE:
		return boolVal(f1 <= f2), nil
	case sTokGT:
		return boolVal(f1 > f2), nil
	case sTokGTE:
		return boolVal(f1 >= f2), nil
	}
	return boolVal(false), nil
}

func evalNumArith(op sTokKind, a, b scriptVal, line int) (scriptVal, error) {
	isF1, i1, f1, err := a.asNumber()
	if err != nil {
		return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", line, err)
	}
	isF2, i2, f2, err := b.asNumber()
	if err != nil {
		return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", line, err)
	}
	if !isF1 && !isF2 {
		switch op {
		case sTokPlus:
			return intVal(i1 + i2), nil
		case sTokMinus:
			return intVal(i1 - i2), nil
		case sTokMul:
			return intVal(i1 * i2), nil
		case sTokDiv:
			if i2 == 0 {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：除数不能为 0", line)
			}
			if i1%i2 == 0 {
				return intVal(i1 / i2), nil
			}
			return floatVal(float64(i1) / float64(i2)), nil
		case sTokMod:
			if i2 == 0 {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：取模除数不能为 0", line)
			}
			return intVal(i1 % i2), nil
		}
	}
	if op == sTokMod {
		if i2 == 0 {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：取模除数不能为 0", line)
		}
		return intVal(i1 % i2), nil
	}
	switch op {
	case sTokPlus:
		return floatVal(f1 + f2), nil
	case sTokMinus:
		return floatVal(f1 - f2), nil
	case sTokMul:
		return floatVal(f1 * f2), nil
	case sTokDiv:
		if f2 == 0 {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：除数不能为 0", line)
		}
		return floatVal(f1 / f2), nil
	}
	return nullVal(), nil
}

func (r *scriptRunner) evalCall(c *exprCall) (scriptVal, error) {
	args := make([]scriptVal, len(c.args))
	for i, ax := range c.args {
		v, err := r.eval(ax)
		if err != nil {
			return scriptVal{}, err
		}
		args[i] = v
	}
	switch c.name {
	case "hmac_sha256":
		mac := hmac.New(sha256.New, []byte(args[0].asString()))
		mac.Write([]byte(args[1].asString()))
		return strVal(hex.EncodeToString(mac.Sum(nil)))
	case "hmac_sha256_base64":
		mac := hmac.New(sha256.New, []byte(args[0].asString()))
		mac.Write([]byte(args[1].asString()))
		return strVal(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	case "sha256":
		sum := sha256.Sum256([]byte(args[0].asString()))
		return strVal(hex.EncodeToString(sum[:]))
	case "md5":
		sum := md5.Sum([]byte(args[0].asString()))
		return strVal(hex.EncodeToString(sum[:]))
	case "base64_encode":
		return strVal(base64.StdEncoding.EncodeToString([]byte(args[0].asString())))
	case "base64_decode":
		raw, err := base64.StdEncoding.DecodeString(args[0].asString())
		if err != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：base64_decode 失败：%w", c.line, err)
		}
		return strVal(string(raw))
	case "url_encode":
		return strVal(url.QueryEscape(args[0].asString()))
	case "url_decode":
		s, err := url.QueryUnescape(args[0].asString())
		if err != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：url_decode 失败：%w", c.line, err)
		}
		return strVal(s)
	case "hex_encode":
		return strVal(hex.EncodeToString([]byte(args[0].asString())))
	case "json_get":
		jp := c.jsonPath
		if jp == nil {
			compiled, err := CompileJSONPath(args[1].asString())
			if err != nil {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：json_get 路径无效：%w", c.line, err)
			}
			jp = compiled
		}
		if raw, ok := jp.Lookup([]byte(args[0].asString())); ok {
			if out, ok := AppendJSONText(nil, raw); ok {
				return strVal(string(out))
			}
		}
		return nullVal(), nil
	case "len":
		return intVal(int64(len(args[0].asString()))), nil
	case "upper":
		return strVal(strings.ToUpper(args[0].asString()))
	case "lower":
		return strVal(strings.ToLower(args[0].asString()))
	case "trim":
		return strVal(strings.TrimSpace(args[0].asString()))
	case "contains":
		return boolVal(strings.Contains(args[0].asString(), args[1].asString())), nil
	case "starts_with":
		return boolVal(strings.HasPrefix(args[0].asString(), args[1].asString())), nil
	case "ends_with":
		return boolVal(strings.HasSuffix(args[0].asString(), args[1].asString())), nil
	case "replace":
		return strVal(strings.ReplaceAll(args[0].asString(), args[1].asString(), args[2].asString()))
	case "substr":
		s := args[0].asString()
		_, start, _, err := args[1].asNumber()
		if err != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：substr 起始下标无效：%v", c.line, err)
		}
		runes := []rune(s)
		if start < 0 {
			start = 0
		}
		if int(start) >= len(runes) {
			return strVal("")
		}
		end := len(runes)
		if len(args) == 3 {
			_, ln, _, err := args[2].asNumber()
			if err != nil {
				return scriptVal{}, fmt.Errorf("脚本第 %d 行：substr 长度无效：%v", c.line, err)
			}
			if ln <= 0 {
				return strVal("")
			}
			if int(start)+int(ln) < end {
				end = int(start) + int(ln)
			}
		}
		return strVal(string(runes[int(start):end]))
	case "to_string":
		return strVal(args[0].asString())
	case "to_number":
		isF, iv, fv, err := args[0].asNumber()
		if err != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：%v", c.line, err)
		}
		if isF {
			return floatVal(fv), nil
		}
		return intVal(iv), nil
	case "now_ms":
		return intVal(time.Now().UnixMilli()), nil
	case "now_sec":
		return intVal(time.Now().Unix()), nil
	case "uuid":
		return strVal(string(appendUUID(nil, r.randSource())))
	case "random_int":
		_, minV, _, err1 := args[0].asNumber()
		_, maxV, _, err2 := args[1].asNumber()
		if err1 != nil || err2 != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：random_int 参数必须是整数", c.line)
		}
		if minV > maxV {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：random_int 最小值 %d 不能大于最大值 %d", c.line, minV, maxV)
		}
		if minV == maxV {
			return intVal(minV), nil
		}
		return intVal(minV + r.randSource().Int64N(maxV-minV+1)), nil
	case "random_string":
		_, nV, _, err := args[0].asNumber()
		if err != nil {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：random_string 参数必须是整数", c.line)
		}
		if nV < 0 || nV > MaxScriptStringBytes {
			return scriptVal{}, fmt.Errorf("脚本第 %d 行：random_string 长度 %d 超出范围", c.line, nV)
		}
		return strVal(string(appendRandString(nil, r.randSource(), int(nV))))
	case "assert":
		passed := args[0].truthy()
		msg := "断言条件为假"
		if len(args) == 2 && strings.TrimSpace(args[1].asString()) != "" {
			msg = args[1].asString()
		}
		if passed {
			r.asserts = append(r.asserts, ScriptAssertOutcome{Passed: true, Message: msg})
			return boolVal(true), nil
		}
		r.asserts = append(r.asserts, ScriptAssertOutcome{Passed: false, Message: msg})
		if r.stopOnAssertFail {
			return scriptVal{}, fmt.Errorf("%w：%s", ErrScriptAssert, msg)
		}
		return boolVal(false), nil
	}
	return nullVal(), nil
}

// Keep utf8 imported only if needed by future extensions; verify rune safety above.
var _ = utf8.RuneError
