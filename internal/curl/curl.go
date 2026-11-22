// Package curl parses cURL command lines into store API drafts and formats
// Tidegate API definitions as reproducible cURL commands for Bash and PowerShell.
package curl

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// Modes and shells accepted by Format and FormatAPI.
const (
	ModeExpanded = "expanded"
	ModeTemplate = "template"

	ShellBash       = "bash"
	ShellPowerShell = "powershell"
)

// FormatOptions configures formatting an HTTP request into a cURL command.
type FormatOptions struct {
	Method        string
	URL           string
	Headers       []metrics.Header
	Body          string
	FormFields    store.KeyValueList
	FormFiles     []scenario.BodyFile
	Multipart     bool
	NoBuffer      bool
	Shell         string // "bash" (default) or "powershell"
	RedactSecrets bool
}

// Parse parses a cURL command string (Bash, Windows CMD, or PowerShell syntax)
// into a store.API draft suitable for the API editor.
func Parse(input string) (*store.API, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("cURL 命令为空")
	}
	if strings.EqualFold(tokens[0], "curl") || strings.EqualFold(tokens[0], "curl.exe") {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return nil, errors.New("cURL 命令缺少请求地址")
	}

	var (
		method     string
		rawURL     string
		headers    store.KeyValueList
		dataParts  []string
		rawData    bool
		isJSONFlag bool
		useGet     bool
		headFlag   bool
		formFields store.KeyValueList
		formFiles  []scenario.BodyFile
		isMulti    bool
		basicAuth  *store.Auth
		stopFlags  bool
	)

	appendHeader := func(line string) {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			name := strings.TrimSpace(strings.TrimSuffix(line, ";"))
			if name != "" {
				headers = append(headers, store.KeyValue{Name: name, Value: ""})
			}
			return
		}
		name := strings.TrimSpace(k)
		if name == "" {
			return
		}
		val := strings.TrimLeft(v, " \t")
		headers = append(headers, store.KeyValue{Name: name, Value: scenario.Scalar(val)})
	}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !stopFlags && tok == "--" {
			stopFlags = true
			continue
		}
		if stopFlags || !strings.HasPrefix(tok, "-") || tok == "-" {
			if rawURL == "" {
				rawURL = trimAngleBrackets(tok)
			}
			continue
		}

		var name, inlineVal string
		var hasInline bool
		if strings.HasPrefix(tok, "--") {
			if k, v, ok := strings.Cut(tok, "="); ok {
				name, inlineVal, hasInline = k, v, true
			} else {
				name = tok
			}
		} else if len(tok) > 2 {
			// Attached short flag such as -XPOST or -HContent-Type:...
			short := tok[:2]
			if shortTakesValue(short) {
				name, inlineVal, hasInline = short, tok[2:], true
			} else {
				// Cluster of boolean short flags like -sSLG.
				for _, r := range tok[1:] {
					switch r {
					case 'G':
						useGet = true
					case 'I':
						headFlag = true
					}
				}
				continue
			}
		} else {
			name = tok
		}

		nextVal := func() (string, bool) {
			if hasInline {
				return inlineVal, true
			}
			if i+1 < len(tokens) {
				i++
				return tokens[i], true
			}
			return "", false
		}

		switch name {
		case "-X", "--request":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少请求方法", name)
			}
			method = strings.ToUpper(strings.TrimSpace(v))
		case "--url":
			v, ok := nextVal()
			if !ok {
				return nil, errors.New("参数 --url 缺少地址")
			}
			rawURL = trimAngleBrackets(v)
		case "-H", "--header":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少请求头内容", name)
			}
			appendHeader(v)
		case "-A", "--user-agent":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少 User-Agent", name)
			}
			headers = append(headers, store.KeyValue{Name: "User-Agent", Value: scenario.Scalar(v)})
		case "-b", "--cookie":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少 Cookie", name)
			}
			headers = append(headers, store.KeyValue{Name: "Cookie", Value: scenario.Scalar(v)})
		case "-e", "--referer":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少 Referer", name)
			}
			headers = append(headers, store.KeyValue{Name: "Referer", Value: scenario.Scalar(v)})
		case "-u", "--user":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少用户名或密码", name)
			}
			user, pass, _ := strings.Cut(v, ":")
			basicAuth = &store.Auth{Type: store.AuthBasic, Username: user, Password: pass}
		case "-d", "--data", "--data-raw", "--data-ascii", "--data-binary":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少请求体内容", name)
			}
			rawData = true
			dataParts = append(dataParts, v)
		case "--data-urlencode":
			v, ok := nextVal()
			if !ok {
				return nil, errors.New("参数 --data-urlencode 缺少数据")
			}
			rawData = true
			dataParts = append(dataParts, encodeURLEncodedItem(v))
		case "--json":
			v, ok := nextVal()
			if !ok {
				return nil, errors.New("参数 --json 缺少 JSON 内容")
			}
			rawData = true
			isJSONFlag = true
			dataParts = append(dataParts, v)
		case "-F", "--form", "--form-string":
			v, ok := nextVal()
			if !ok {
				return nil, fmt.Errorf("参数 %s 缺少表单字段", name)
			}
			isMulti = true
			k, val, hasEq := strings.Cut(v, "=")
			if hasEq {
				k = strings.TrimSpace(k)
				if name != "--form-string" && strings.HasPrefix(val, "@") {
					filePath := strings.TrimPrefix(val, "@")
					if semi := strings.IndexByte(filePath, ';'); semi >= 0 {
						filePath = filePath[:semi]
					}
					filePath = strings.Trim(filePath, `"'`)
					base := filepath.Base(strings.ReplaceAll(filePath, "\\", "/"))
					formFiles = append(formFiles, scenario.BodyFile{Field: k, Path: base})
				} else {
					formFields = append(formFields, store.KeyValue{Name: k, Value: scenario.Scalar(val)})
				}
			}
		case "-G", "--get":
			useGet = true
		case "-I", "--head":
			headFlag = true
		case "-o", "--output", "-m", "--max-time", "--connect-timeout", "--retry", "--retry-delay",
			"-x", "--proxy", "-U", "--proxy-user", "--resolve", "--cacert", "--cert", "--key", "-w", "--write-out":
			_, _ = nextVal()
		}
	}

	if rawURL == "" {
		return nil, errors.New("未在 cURL 命令中找到请求 URL")
	}

	baseURL, queryList := splitURLQuery(rawURL)

	// Decide effective method.
	if method == "" {
		switch {
		case headFlag:
			method = "HEAD"
		case useGet:
			method = "GET"
		case rawData || isMulti:
			method = "POST"
		default:
			method = "GET"
		}
	}

	// Handle -G (--get): move -d parts into query parameters instead of body.
	if useGet && len(dataParts) > 0 {
		for _, part := range dataParts {
			for _, kv := range parseFormPairs(part) {
				queryList = append(queryList, kv)
			}
		}
		dataParts = nil
		rawData = false
	}

	// Ensure --json defaults.
	if isJSONFlag {
		if !hasHeader(headers, "Content-Type") {
			headers = append(headers, store.KeyValue{Name: "Content-Type", Value: "application/json"})
		}
		if !hasHeader(headers, "Accept") {
			headers = append(headers, store.KeyValue{Name: "Accept", Value: "application/json"})
		}
	}

	// Extract Authorization Bearer / Basic into structured Auth when absent.
	var extractedAuth *store.Auth
	if basicAuth != nil {
		extractedAuth = basicAuth
	} else {
		var kept store.KeyValueList
		for _, h := range headers {
			if extractedAuth == nil && strings.EqualFold(h.Name, "Authorization") {
				if a, ok := parseAuthHeader(string(h.Value)); ok {
					extractedAuth = a
					continue
				}
			}
			kept = append(kept, h)
		}
		headers = kept
	}

	var body *store.APIBody
	switch {
	case isMulti:
		body = &store.APIBody{
			Type:   "multipart",
			Fields: formFields,
			Files:  formFiles,
		}
	case rawData:
		combined := strings.Join(dataParts, "&")
		ct := headerValue(headers, "Content-Type")
		ctLower := strings.ToLower(ct)
		switch {
		case isJSONFlag || strings.Contains(ctLower, "json") || (ct == "" && looksLikeJSON(combined)):
			body = &store.APIBody{
				Type:    "json",
				Content: combined,
			}
		case strings.Contains(ctLower, "application/x-www-form-urlencoded"):
			body = &store.APIBody{
				Type:   "form",
				Fields: parseFormPairs(combined),
			}
		default:
			body = &store.APIBody{
				Type:        "raw",
				Content:     combined,
				ContentType: ct,
			}
		}
	}

	api := &store.API{
		Name: deriveAPIName(method, baseURL),
		Request: store.APIRequest{
			Method:  method,
			URL:     baseURL,
			Query:   queryList,
			Headers: headers,
			Body:    body,
		},
		Auth: extractedAuth,
	}
	return api, nil
}

// Format formats explicit request parameters as a cURL command for bash or powershell.
func Format(opts FormatOptions) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(opts.Method))
	if method == "" {
		method = "GET"
	}
	rawURL := strings.TrimSpace(opts.URL)
	if rawURL == "" {
		return "", errors.New("URL 不能为空")
	}
	shell := strings.ToLower(strings.TrimSpace(opts.Shell))
	if shell == "" {
		shell = ShellBash
	}
	if shell != ShellBash && shell != ShellPowerShell {
		return "", fmt.Errorf("不支持的脚本类型 %q，可选 bash 或 powershell", opts.Shell)
	}

	quote := quoteBash
	cont := " \\\n  "
	cmd := "curl"
	if shell == ShellPowerShell {
		quote = quotePowerShell
		cont = " `\n  "
		cmd = "curl.exe"
	}

	prefix := cmd
	if opts.NoBuffer {
		prefix += " -N"
	}
	parts := []string{prefix + " -X " + method + " " + quote(rawURL)}
	for _, h := range opts.Headers {
		if strings.TrimSpace(h.Name) == "" {
			continue
		}
		val := h.Value
		if opts.RedactSecrets {
			val = redactHeaderValue(h.Name, val)
		}
		parts = append(parts, "-H "+quote(h.Name+": "+val))
	}

	if opts.Multipart {
		for _, f := range opts.FormFields {
			if f.Disabled || strings.TrimSpace(f.Name) == "" {
				continue
			}
			val := string(f.Value)
			if opts.RedactSecrets && f.Secret {
				val = "<REDACTED>"
			}
			parts = append(parts, "-F "+quote(strings.TrimSpace(f.Name)+"="+val))
		}
		for _, bf := range opts.FormFiles {
			if bf.Disabled || strings.TrimSpace(bf.Field) == "" || strings.TrimSpace(bf.Path) == "" {
				continue
			}
			parts = append(parts, "-F "+quote(strings.TrimSpace(bf.Field)+"=@"+filepath.Base(bf.Path)))
		}
	} else if opts.Body != "" {
		parts = append(parts, "--data-raw "+quote(opts.Body))
	}

	return strings.Join(parts, cont), nil
}

// FormatAPI converts a store.API (with optional resolved folder/environment
// preview context) into a cURL command in "expanded" or "template" mode.
func FormatAPI(api *store.API, preview *store.EffectivePreview, mode, shell string, redact bool) (string, error) {
	if api == nil {
		return "", errors.New("接口定义不能为空")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = ModeExpanded
	}
	if mode != ModeExpanded && mode != ModeTemplate {
		return "", fmt.Errorf("不支持的导出模式 %q，可选 expanded 或 template", mode)
	}

	varVals := map[string]string{}
	secretVars := map[string]bool{}
	baseURL := ""
	var inherited []store.InheritedHeader
	var effAuth *store.Auth

	if preview != nil {
		baseURL = preview.BaseURL
		inherited = preview.InheritedHeaders
		effAuth = preview.EffectiveAuth.Auth
		for _, v := range preview.Variables {
			varVals[v.Name] = v.Value
			if v.Secret {
				secretVars[v.Name] = true
			}
		}
	} else {
		effAuth = api.Auth
	}

	expand := func(s string) string {
		if mode == ModeTemplate {
			return s
		}
		return expandCurlTemplate(s, varVals, secretVars, redact)
	}

	proto := strings.ToLower(strings.TrimSpace(api.Request.Protocol))
	if proto == "grpc" {
		return "", errors.New("gRPC 接口不支持导出为 cURL 命令，请使用工作台直接调试")
	}

	method := strings.ToUpper(strings.TrimSpace(api.Request.Method))
	if method == "" {
		method = "GET"
	}

	// Assemble URL with path params, environment base URL, and query params.
	rawURL := strings.TrimSpace(api.Request.URL)
	if rawURL == "" && preview != nil && preview.URL != "" {
		rawURL = preview.URL
	}
	rawURL = applyPathParamsSimple(rawURL, api.Request.PathParams, expand)
	rawURL = expand(rawURL)
	lower := strings.ToLower(rawURL)
	hasKnownScheme := strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "ws://") ||
		strings.HasPrefix(lower, "wss://")
	if !hasKnownScheme && baseURL != "" && rawURL != "" && (mode == ModeExpanded || !strings.HasPrefix(rawURL, "{{")) {
		joined := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(rawURL, "/")
		if proto == "ws" {
			if strings.HasPrefix(strings.ToLower(joined), "http://") {
				joined = "ws://" + joined[7:]
			} else if strings.HasPrefix(strings.ToLower(joined), "https://") {
				joined = "wss://" + joined[8:]
			}
		}
		rawURL = joined
	}

	// Collect headers: inherited (not overridden) -> auth -> API headers.
	var headers []metrics.Header
	for _, ih := range inherited {
		if ih.Overridden {
			continue
		}
		headers = append(headers, metrics.Header{Name: ih.Name, Value: expand(ih.Value)})
	}

	var extraQuery []store.KeyValue
	if effAuth != nil {
		switch effAuth.Type {
		case store.AuthBearer:
			tok := expand(effAuth.Token)
			if redact && !containsTemplate(tok) {
				tok = "<REDACTED>"
			}
			headers = upsertHeader(headers, "Authorization", "Bearer "+tok)
		case store.AuthBasic:
			user := expand(effAuth.Username)
			pass := expand(effAuth.Password)
			if redact && (!containsTemplate(user) || !containsTemplate(pass)) {
				headers = upsertHeader(headers, "Authorization", "Basic <REDACTED>")
			} else if mode == ModeTemplate && (containsTemplate(user) || containsTemplate(pass)) {
				headers = upsertHeader(headers, "Authorization", "Basic <base64("+user+":"+pass+")>")
			} else {
				cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
				headers = upsertHeader(headers, "Authorization", "Basic "+cred)
			}
		case store.AuthAPIKey:
			if strings.TrimSpace(effAuth.Key) != "" {
				val := expand(effAuth.Value)
				if redact && !containsTemplate(val) {
					val = "<REDACTED>"
				}
				if effAuth.In == "query" {
					extraQuery = append(extraQuery, store.KeyValue{Name: effAuth.Key, Value: scenario.Scalar(val)})
				} else {
					headers = upsertHeader(headers, effAuth.Key, val)
				}
			}
		}
	}

	for _, h := range api.Request.Headers {
		if h.Disabled || strings.TrimSpace(h.Name) == "" {
			continue
		}
		val := expand(string(h.Value))
		if redact && (h.Secret || (isSensitiveHeader(h.Name) && !containsTemplate(val))) {
			val = redactHeaderValue(h.Name, val)
		}
		headers = append(headers, metrics.Header{Name: strings.TrimSpace(h.Name), Value: val})
	}

	// Append active query parameters + API Key query parameter.
	allQuery := append([]store.KeyValue(nil), api.Request.Query...)
	allQuery = append(allQuery, extraQuery...)
	rawURL = appendQueryParams(rawURL, allQuery, expand, redact)

	if proto == "sse" && !hasMetricHeader(headers, "Accept") {
		headers = append(headers, metrics.Header{Name: "Accept", Value: "text/event-stream"})
	}

	opts := FormatOptions{
		Method:        method,
		URL:           rawURL,
		Headers:       headers,
		NoBuffer:      proto == "sse",
		Shell:         shell,
		RedactSecrets: redact,
	}

	if b := api.Request.Body; b != nil && b.Type != "" && b.Type != "none" {
		switch b.Type {
		case "json":
			if !hasMetricHeader(headers, "Content-Type") {
				opts.Headers = append(opts.Headers, metrics.Header{Name: "Content-Type", Value: "application/json"})
			}
			opts.Body = expand(b.Content)
		case "raw":
			if b.ContentType != "" && !hasMetricHeader(headers, "Content-Type") {
				opts.Headers = append(opts.Headers, metrics.Header{Name: "Content-Type", Value: b.ContentType})
			}
			opts.Body = expand(b.Content)
		case "binary":
			ct := b.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			if !hasMetricHeader(headers, "Content-Type") {
				opts.Headers = append(opts.Headers, metrics.Header{Name: "Content-Type", Value: ct})
			}
			if b.File != "" {
				// Best-effort data-binary hint matching --data-binary.
				opts.Body = "@" + filepath.Base(b.File)
			}
		case "form":
			if !hasMetricHeader(headers, "Content-Type") {
				opts.Headers = append(opts.Headers, metrics.Header{Name: "Content-Type", Value: "application/x-www-form-urlencoded"})
			}
			var pairs []string
			for _, f := range b.Fields {
				if f.Disabled || strings.TrimSpace(f.Name) == "" {
					continue
				}
				val := expand(string(f.Value))
				if redact && f.Secret && !containsTemplate(val) {
					val = "<REDACTED>"
				}
				pairs = append(pairs, url.QueryEscape(strings.TrimSpace(f.Name))+"="+encodePreservingTemplates(val))
			}
			opts.Body = strings.Join(pairs, "&")
		case "multipart":
			opts.Multipart = true
			for _, f := range b.Fields {
				if f.Disabled || strings.TrimSpace(f.Name) == "" {
					continue
				}
				val := expand(string(f.Value))
				opts.FormFields = append(opts.FormFields, store.KeyValue{
					Name:   f.Name,
					Value:  scenario.Scalar(val),
					Secret: f.Secret,
				})
			}
			for _, bf := range b.Files {
				if bf.Disabled {
					continue
				}
				opts.FormFiles = append(opts.FormFiles, bf)
			}
		}
	}

	return Format(opts)
}

func tokenize(input string) ([]string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, nil
	}
	for _, p := range []string{"PS> ", "PS ", "$ ", "> "} {
		if strings.HasPrefix(s, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	isPS := strings.Contains(s, "`\n") || strings.Contains(s, "`\r\n") ||
		strings.HasPrefix(strings.ToLower(s), "curl.exe")
	isCMD := strings.Contains(s, "^\n") || strings.Contains(s, "^\r\n") || strings.Contains(s, `^"`)

	var tokens []string
	var cur strings.Builder
	inTok := false
	flush := func() {
		if inTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			inTok = false
		}
	}

	i := 0
	for i < len(s) {
		ch := s[i]
		// Whitespace outside quotes splits tokens.
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			flush()
			i++
			continue
		}
		// Line continuations outside quotes: \, `, ^ followed by optional whitespace? (strictly immediately followed by \r?\n)
		if (ch == '\\' || ch == '`' || ch == '^') && i+1 < len(s) && (s[i+1] == '\n' || (s[i+1] == '\r' && i+2 < len(s) && s[i+2] == '\n')) {
			i++
			if s[i] == '\r' {
				i++
			}
			i++
			continue
		}

		inTok = true

		// CMD ^"..."^" quoting (Chrome DevTools Windows CMD export style).
		if isCMD && ch == '^' && i+1 < len(s) {
			next := s[i+1]
			if next == '"' {
				i += 2
				closed := false
				for i < len(s) {
					c := s[i]
					if c == '^' && i+1 < len(s) {
						n := s[i+1]
						if n == '\n' || (n == '\r' && i+2 < len(s) && s[i+2] == '\n') {
							i++
							if s[i] == '\r' {
								i++
							}
							i++
							continue
						}
						if n == '"' {
							closed = true
							i += 2
							break
						}
						if n == '\\' && i+3 < len(s) && s[i+2] == '^' && s[i+3] == '"' {
							cur.WriteByte('"')
							i += 4
							continue
						}
						if n == '\\' && i+2 < len(s) && s[i+2] == '"' {
							cur.WriteByte('"')
							i += 3
							continue
						}
						cur.WriteByte(n)
						i += 2
						continue
					}
					if c == '"' {
						closed = true
						i++
						break
					}
					cur.WriteByte(c)
					i++
				}
				if !closed {
					return nil, errors.New("cURL 命令存在未闭合的引号")
				}
				continue
			}
			cur.WriteByte(next)
			i += 2
			continue
		}

		// ANSI-C quoting $'...'
		if ch == '$' && i+1 < len(s) && s[i+1] == '\'' {
			i += 2
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\'' {
					closed = true
					i++
					break
				}
				if c == '\\' && i+1 < len(s) {
					i++
					esc := s[i]
					switch esc {
					case 'n':
						cur.WriteByte('\n')
					case 'r':
						cur.WriteByte('\r')
					case 't':
						cur.WriteByte('\t')
					case 'a':
						cur.WriteByte('\a')
					case 'b':
						cur.WriteByte('\b')
					case 'f':
						cur.WriteByte('\f')
					case 'v':
						cur.WriteByte('\v')
					case '\\', '\'', '"':
						cur.WriteByte(esc)
					case 'x':
						if i+2 < len(s) {
							if v, ok := parseHexByte(s[i+1], s[i+2]); ok {
								cur.WriteByte(v)
								i += 2
								break
							}
						}
						cur.WriteString(`\x`)
					default:
						cur.WriteByte(esc)
					}
					i++
					continue
				}
				cur.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("cURL 命令存在未闭合的单引号")
			}
			continue
		}

		// Single quote literal string.
		if ch == '\'' {
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\'' {
					// PowerShell single-quote escape uses ''.
					if isPS && i+1 < len(s) && s[i+1] == '\'' {
						cur.WriteByte('\'')
						i += 2
						continue
					}
					closed = true
					i++
					break
				}
				cur.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("cURL 命令存在未闭合的单引号")
			}
			continue
		}

		// Double quote string.
		if ch == '"' {
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '"' {
					closed = true
					i++
					break
				}
				if isPS && c == '`' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						cur.WriteByte('\n')
					case 'r':
						cur.WriteByte('\r')
					case 't':
						cur.WriteByte('\t')
					case '\n':
					case '\r':
						if i+1 < len(s) && s[i+1] == '\n' {
							i++
						}
					default:
						cur.WriteByte(s[i])
					}
					i++
					continue
				}
				if c == '\\' && i+1 < len(s) {
					n := s[i+1]
					switch n {
					case '"', '\\', '$', '`':
						cur.WriteByte(n)
						i += 2
						continue
					case '\n':
						i += 2
						continue
					case '\r':
						if i+2 < len(s) && s[i+2] == '\n' {
							i += 3
							continue
						}
					}
				}
				if isCMD && c == '^' && i+1 < len(s) {
					n := s[i+1]
					if n == '"' || n == '^' || n == '&' || n == '%' {
						cur.WriteByte(n)
						i += 2
						continue
					}
				}
				cur.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("cURL 命令存在未闭合的双引号")
			}
			continue
		}

		// Unquoted backslash escape (non-Windows paths usually quoted).
		if ch == '\\' && i+1 < len(s) {
			i++
			cur.WriteByte(s[i])
			i++
			continue
		}

		cur.WriteByte(ch)
		i++
	}
	flush()
	return tokens, nil
}

func parseHexByte(a, b byte) (byte, bool) {
	hi, ok1 := hexNibble(a)
	lo, ok2 := hexNibble(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return (hi << 4) | lo, true
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func shortTakesValue(short string) bool {
	switch short {
	case "-X", "-H", "-d", "-F", "-u", "-A", "-b", "-e", "-o", "-m", "-x", "-U", "-w":
		return true
	}
	return false
}

func trimAngleBrackets(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s[1 : len(s)-1]
	}
	return s
}

func encodeURLEncodedItem(v string) string {
	if strings.HasPrefix(v, "=") {
		return url.QueryEscape(v[1:])
	}
	if k, val, ok := strings.Cut(v, "="); ok {
		return url.QueryEscape(k) + "=" + encodePreservingTemplates(val)
	}
	return encodePreservingTemplates(v)
}

func splitURLQuery(rawURL string) (string, store.KeyValueList) {
	noFrag := rawURL
	if h := strings.IndexByte(noFrag, '#'); h >= 0 {
		noFrag = noFrag[:h]
	}
	qIdx := strings.IndexByte(noFrag, '?')
	if qIdx < 0 {
		return noFrag, nil
	}
	base := noFrag[:qIdx]
	rawQ := noFrag[qIdx+1:]
	if rawQ == "" {
		return base, nil
	}
	return base, parseFormPairs(rawQ)
}

func parseFormPairs(raw string) store.KeyValueList {
	var out store.KeyValueList
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, store.KeyValue{
			Name:  decodePreservingTemplates(k),
			Value: scenario.Scalar(decodePreservingTemplates(v)),
		})
	}
	return out
}

// decodePreservingTemplates unescapes URL-encoded segments without altering
// literal {{variable}} placeholders.
func decodePreservingTemplates(s string) string {
	if !strings.Contains(s, "{{") {
		if dec, err := url.QueryUnescape(s); err == nil {
			return dec
		}
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, "{{")
		if start < 0 {
			if dec, err := url.QueryUnescape(s); err == nil {
				b.WriteString(dec)
			} else {
				b.WriteString(s)
			}
			break
		}
		if start > 0 {
			seg := s[:start]
			if dec, err := url.QueryUnescape(seg); err == nil {
				b.WriteString(dec)
			} else {
				b.WriteString(seg)
			}
		}
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString(s[start:])
			break
		}
		b.WriteString(s[start : start+2+end+2])
		s = s[start+2+end+2:]
	}
	return b.String()
}

func encodePreservingTemplates(s string) string {
	if !strings.Contains(s, "{{") {
		return url.QueryEscape(s)
	}
	var b strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, "{{")
		if start < 0 {
			b.WriteString(url.QueryEscape(s))
			break
		}
		if start > 0 {
			b.WriteString(url.QueryEscape(s[:start]))
		}
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString(url.QueryEscape(s[start:]))
			break
		}
		b.WriteString(s[start : start+2+end+2])
		s = s[start+2+end+2:]
	}
	return b.String()
}

func hasHeader(hs store.KeyValueList, name string) bool {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

func hasMetricHeader(hs []metrics.Header, name string) bool {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

func headerValue(hs store.KeyValueList, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return string(h.Value)
		}
	}
	return ""
}

func parseAuthHeader(val string) (*store.Auth, bool) {
	trimmed := strings.TrimSpace(val)
	if len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "bearer ") {
		tok := strings.TrimSpace(trimmed[7:])
		if tok != "" {
			return &store.Auth{Type: store.AuthBearer, Token: tok}, true
		}
	}
	if len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(trimmed[6:]))
		if err == nil {
			u, p, ok := strings.Cut(string(raw), ":")
			if ok {
				return &store.Auth{Type: store.AuthBasic, Username: u, Password: p}, true
			}
		}
	}
	return nil, false
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if json.Valid([]byte(t)) {
		return (t[0] == '{' || t[0] == '[')
	}
	// JSON containing unexpanded {{var}} placeholders often starts with { or [.
	return (t[0] == '{' || t[0] == '[') && strings.Contains(t, "{{")
}

func deriveAPIName(method, rawURL string) string {
	sanitized := replaceTemplatesForParse(rawURL)
	u, err := url.Parse(sanitized)
	if err == nil {
		p := strings.TrimSpace(u.Path)
		if p != "" && p != "/" {
			if len(p) > 60 {
				p = p[:60]
			}
			return method + " " + p
		}
		if u.Host != "" {
			return method + " " + u.Host
		}
	}
	return method + " 接口"
}

func replaceTemplatesForParse(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, "{{")
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString("var")
			break
		}
		b.WriteString("var")
		s = s[start+2+end+2:]
	}
	return b.String()
}

func quoteBash(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quotePowerShell(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, "\r\n") {
		var b strings.Builder
		b.WriteByte('"')
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '`':
				b.WriteString("``")
			case '"':
				b.WriteString("`\"")
			case '$':
				b.WriteString("`$")
			case '\r':
				b.WriteString("`r")
			case '\n':
				b.WriteString("`n")
			default:
				b.WriteByte(s[i])
			}
		}
		b.WriteByte('"')
		return b.String()
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func containsTemplate(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

func expandCurlTemplate(s string, vars map[string]string, secrets map[string]bool, redact bool) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, "{{")
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		end := strings.Index(s[start+2:], "}}")
		if end < 0 {
			b.WriteString(s[start:])
			break
		}
		key := strings.TrimSpace(s[start+2 : start+2+end])
		if val, ok := vars[key]; ok {
			if redact && secrets[key] {
				b.WriteString("<REDACTED>")
			} else {
				b.WriteString(val)
			}
		} else {
			b.WriteString(s[start : start+2+end+2])
		}
		s = s[start+2+end+2:]
	}
	return b.String()
}

func applyPathParamsSimple(rawURL string, params store.KeyValueList, expand func(string) string) string {
	if len(params) == 0 || rawURL == "" {
		return rawURL
	}
	vals := make(map[string]string, len(params))
	for _, p := range params {
		if p.Disabled || strings.TrimSpace(p.Name) == "" {
			continue
		}
		vals[strings.TrimSpace(p.Name)] = expand(string(p.Value))
	}
	if len(vals) == 0 {
		return rawURL
	}
	for k, v := range vals {
		rawURL = strings.ReplaceAll(rawURL, "{"+k+"}", v)
	}
	var out strings.Builder
	for i := 0; i < len(rawURL); {
		if rawURL[i] == ':' && (i == 0 || rawURL[i-1] == '/') {
			j := i + 1
			for j < len(rawURL) && isIdentRune(rawURL[j]) {
				j++
			}
			if j > i+1 {
				name := rawURL[i+1 : j]
				if v, ok := vals[name]; ok {
					out.WriteString(v)
					i = j
					continue
				}
			}
		}
		out.WriteByte(rawURL[i])
		i++
	}
	return out.String()
}

func isIdentRune(c byte) bool {
	return c == '_' || c == '-' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
}

func appendQueryParams(rawURL string, qs store.KeyValueList, expand func(string) string, redact bool) string {
	if len(qs) == 0 {
		return rawURL
	}
	var pairs []string
	for _, q := range qs {
		if q.Disabled || strings.TrimSpace(q.Name) == "" {
			continue
		}
		val := expand(string(q.Value))
		if redact && q.Secret && !containsTemplate(val) {
			val = "<REDACTED>"
		}
		pairs = append(pairs, url.QueryEscape(strings.TrimSpace(q.Name))+"="+encodePreservingTemplates(val))
	}
	if len(pairs) == 0 {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + strings.Join(pairs, "&")
}

func upsertHeader(hs []metrics.Header, name, value string) []metrics.Header {
	for i := range hs {
		if strings.EqualFold(hs[i].Name, name) {
			hs[i].Name = name
			hs[i].Value = value
			return hs
		}
	}
	return append(hs, metrics.Header{Name: name, Value: value})
}

func isSensitiveHeader(name string) bool {
	return metrics.RedactHeader(name, "x") == "***"
}

func redactHeaderValue(name, value string) string {
	if !isSensitiveHeader(name) {
		return value
	}
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "bearer ") {
		if containsTemplate(trimmed[7:]) {
			return value
		}
		return "Bearer <REDACTED>"
	}
	if len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "basic ") {
		if containsTemplate(trimmed[6:]) {
			return value
		}
		return "Basic <REDACTED>"
	}
	if containsTemplate(value) {
		return value
	}
	return "<REDACTED>"
}
