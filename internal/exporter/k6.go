package exporter

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

const k6EnvSentinel = "__TIDEGATE_K6_ENV__"

// ExportScenarioK6 converts a stored Tidegate scenario into an executable ES6 k6
// load-test script. Any variable marked Secret is referenced through k6's
// `__ENV.<VAR>` without embedding plaintext credentials.
func ExportScenarioK6(st *store.Store, projectID, scenarioID string, opts store.ResolveOptions) ([]byte, error) {
	pr, err := st.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	sc, err := st.GetScenario(projectID, scenarioID)
	if err != nil {
		return nil, err
	}
	envID := opts.EnvironmentID
	if envID == "" {
		envID = sc.EnvironmentID
	}

	envVars := map[string]string{} // storeVarName -> ENV_VAR_NAME
	markSecrets := func(list store.KeyValueList) {
		for _, kv := range list {
			if kv.Disabled || !kv.Secret {
				continue
			}
			name := strings.TrimSpace(kv.Name)
			if name != "" {
				envVars[name] = toK6EnvName(name)
			}
		}
	}
	markSecrets(pr.Variables)
	if envID != "" {
		if env, err := st.GetEnvironment(projectID, envID); err == nil {
			markSecrets(env.Variables)
		}
	}

	varOverrides := make(map[string]scenario.Scalar, len(opts.Variables)+len(envVars))
	for k, v := range opts.Variables {
		varOverrides[k] = v
	}
	for k, envName := range envVars {
		if _, userSet := opts.Variables[k]; !userSet {
			varOverrides[k] = scenario.Scalar(k6EnvSentinel + envName)
		}
	}

	res, err := st.ResolveScenario(projectID, scenarioID, store.ResolveOptions{
		EnvironmentID:     envID,
		Variables:         varOverrides,
		SkipSecretDecrypt: true,
	})
	if err != nil {
		return nil, err
	}

	def := res.Definition
	var b strings.Builder
	b.WriteString("// 由 Tidegate 导出的 k6 压测脚本\n")
	b.WriteString("import http from 'k6/http';\n")
	b.WriteString("import { check, group, sleep } from 'k6';\n")
	if len(def.Data) > 0 {
		b.WriteString("import { SharedArray } from 'k6/data';\n")
	}
	b.WriteString("\n")

	if len(envVars) > 0 {
		names := make([]string, 0, len(envVars))
		for _, envName := range envVars {
			names = append(names, envName)
		}
		sort.Strings(names)
		b.WriteString("// 敏感变量通过 k6 环境变量注入（示例：k6 run -e ")
		b.WriteString(names[0])
		b.WriteString("=... script.js）\n\n")
	}

	writeK6Options(&b, def)
	writeK6DataArrays(&b, def.Data)
	writeK6Helpers(&b, usesOAuth2InSteps(def.Steps))

	b.WriteString("export default function () {\n")
	writeK6InitVars(&b, def.Variables, envVars)
	for _, ds := range def.Data {
		safeAlias := sanitizeJSIdent(ds.Name)
		b.WriteString(fmt.Sprintf("  const row_%s = data_%s.length > 0 ? data_%s[__ITER %% data_%s.length] : {};\n", safeAlias, safeAlias, safeAlias, safeAlias))
	}
	b.WriteString("\n")

	var seq int
	writeK6Steps(&b, def.Steps, def.Data, envVars, "  ", &seq)

	b.WriteString("}\n")
	return []byte(b.String()), nil
}

func writeK6InitVars(b *strings.Builder, defVars map[string]scenario.Scalar, envVars map[string]string) {
	keys := make([]string, 0, len(defVars))
	for k := range defVars {
		if _, isSecret := envVars[k]; isSecret {
			continue
		}
		if strings.Contains(string(defVars[k]), k6EnvSentinel) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		b.WriteString("  const vars = {};\n")
		return
	}
	sort.Strings(keys)
	b.WriteString("  const vars = {\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("    '%s': '%s',\n", jsEscape(k), jsEscape(string(defVars[k]))))
	}
	b.WriteString("  };\n")
}

func writeK6Options(b *strings.Builder, def *scenario.Definition) {
	b.WriteString("export const options = {\n")
	if def.HTTP.InsecureSkipVerify {
		b.WriteString("  insecureSkipTLSVerify: true,\n")
	}
	if def.HTTP.FollowRedirects != nil && !*def.HTTP.FollowRedirects {
		b.WriteString("  maxRedirects: 0,\n")
	} else if def.HTTP.MaxRedirects != nil {
		b.WriteString(fmt.Sprintf("  maxRedirects: %d,\n", *def.HTTP.MaxRedirects))
	}

	mode := strings.ToLower(strings.TrimSpace(def.Load.Mode))
	if mode == "rate" {
		b.WriteString("  scenarios: {\n")
		b.WriteString("    rate_load: {\n")
		b.WriteString("      executor: 'ramping-arrival-rate',\n")
		b.WriteString("      startRate: 0,\n")
		b.WriteString("      timeUnit: '1s',\n")
		maxVUs := def.Load.MaxVUs
		if maxVUs <= 0 {
			maxVUs = 100
		}
		b.WriteString(fmt.Sprintf("      preAllocatedVUs: %d,\n", max(1, maxVUs/2)))
		b.WriteString(fmt.Sprintf("      maxVUs: %d,\n", maxVUs))
		b.WriteString("      stages: [\n")
		for _, st := range def.Load.Stages {
			b.WriteString(fmt.Sprintf("        { target: %s, duration: '%s' },\n",
				strconv.FormatFloat(st.Target, 'f', -1, 64), k6Duration(st.Duration.D())))
		}
		b.WriteString("      ],\n")
		b.WriteString("    },\n")
		b.WriteString("  },\n")
	} else {
		b.WriteString("  stages: [\n")
		for _, st := range def.Load.Stages {
			b.WriteString(fmt.Sprintf("    { duration: '%s', target: %s },\n",
				k6Duration(st.Duration.D()), strconv.FormatFloat(st.Target, 'f', -1, 64)))
		}
		b.WriteString("  ],\n")
	}

	if len(def.Load.Thresholds) > 0 {
		b.WriteString("  thresholds: {\n")
		for _, th := range def.Load.Thresholds {
			metricName, cond := toK6Threshold(th)
			if metricName != "" && cond != "" {
				b.WriteString(fmt.Sprintf("    '%s': ['%s'],\n", metricName, cond))
			}
		}
		b.WriteString("  },\n")
	}
	b.WriteString("};\n\n")
}

func writeK6DataArrays(b *strings.Builder, data []scenario.DataSource) {
	for _, ds := range data {
		safeAlias := sanitizeJSIdent(ds.Name)
		b.WriteString(fmt.Sprintf("const data_%s = new SharedArray('%s', function () {\n", safeAlias, jsEscape(ds.Name)))
		lower := strings.ToLower(ds.File)
		switch {
		case strings.HasSuffix(lower, ".json"):
			b.WriteString(fmt.Sprintf("  return JSON.parse(open('%s'));\n", jsEscape(ds.File)))
		case strings.HasSuffix(lower, ".jsonl"), strings.HasSuffix(lower, ".ndjson"):
			b.WriteString(fmt.Sprintf("  return open('%s').split('\\n').filter(Boolean).map((l) => JSON.parse(l));\n", jsEscape(ds.File)))
		default:
			b.WriteString(fmt.Sprintf("  const lines = open('%s').trim().split(/\\r?\\n/);\n", jsEscape(ds.File)))
			b.WriteString("  if (lines.length < 2) return [];\n")
			b.WriteString("  const hdr = lines[0].split(',');\n")
			b.WriteString("  return lines.slice(1).map((line) => {\n")
			b.WriteString("    const cols = line.split(',');\n")
			b.WriteString("    const row = {};\n")
			b.WriteString("    hdr.forEach((h, idx) => { row[h.trim()] = (cols[idx] || '').trim(); });\n")
			b.WriteString("    return row;\n")
			b.WriteString("  });\n")
		}
		b.WriteString("});\n\n")
	}
}

func writeK6Helpers(b *strings.Builder, needOAuth2 bool) {
	if !needOAuth2 {
		return
	}
	b.WriteString("const _oauthCache = {};\n")
	b.WriteString("function getOAuth2Token(tokenUrl, clientId, clientSecret, scope, clientAuth) {\n")
	b.WriteString("  const key = tokenUrl + '|' + clientId + '|' + scope;\n")
	b.WriteString("  if (_oauthCache[key] && Date.now() < _oauthCache[key].exp) return _oauthCache[key].tok;\n")
	b.WriteString("  const form = { grant_type: 'client_credentials' };\n")
	b.WriteString("  if (scope) form.scope = scope;\n")
	b.WriteString("  const headers = {};\n")
	b.WriteString("  if (clientAuth === 'body') { form.client_id = clientId; form.client_secret = clientSecret; }\n")
	b.WriteString("  const res = http.post(tokenUrl, form, { headers });\n")
	b.WriteString("  const tok = res.status === 200 ? (res.json('access_token') || '') : '';\n")
	b.WriteString("  _oauthCache[key] = { tok, exp: Date.now() + 300000 };\n")
	b.WriteString("  return tok;\n")
	b.WriteString("}\n\n")
}

func writeK6Steps(b *strings.Builder, steps []scenario.Step, data []scenario.DataSource, envVars map[string]string, indent string, seq *int) {
	dataSet := make(map[string]bool, len(data))
	for _, ds := range data {
		dataSet[ds.Name] = true
	}

	for _, st := range steps {
		switch {
		case st.Group != nil:
			label := firstString(st.Name, st.ID)
			b.WriteString(fmt.Sprintf("%sgroup('%s', function () {\n", indent, jsEscape(label)))
			writeK6Steps(b, st.Group.Children, data, envVars, indent+"  ", seq)
			b.WriteString(fmt.Sprintf("%s});\n", indent))
			writeK6Sleep(b, indent, st.Think)

		case st.Loop != nil:
			iters := st.Loop.Count
			if iters <= 0 {
				iters = st.Loop.MaxIterations
			}
			if iters <= 0 {
				iters = 1
			}
			b.WriteString(fmt.Sprintf("%sfor (let i = 0; i < %d; i++) {\n", indent, iters))
			if strings.TrimSpace(st.Loop.While) != "" {
				b.WriteString(fmt.Sprintf("%s  if (!(%s)) break;\n", indent, toK6Condition(st.Loop.While, dataSet, envVars)))
			}
			writeK6Steps(b, st.Loop.Children, data, envVars, indent+"  ", seq)
			b.WriteString(fmt.Sprintf("%s}\n", indent))
			writeK6Sleep(b, indent, st.Think)

		case st.If != nil:
			b.WriteString(fmt.Sprintf("%sif (%s) {\n", indent, toK6Condition(st.If.Expr, dataSet, envVars)))
			writeK6Steps(b, st.If.Then, data, envVars, indent+"  ", seq)
			if len(st.If.Else) > 0 {
				b.WriteString(fmt.Sprintf("%s} else {\n", indent))
				writeK6Steps(b, st.If.Else, data, envVars, indent+"  ", seq)
			}
			b.WriteString(fmt.Sprintf("%s}\n", indent))
			writeK6Sleep(b, indent, st.Think)

		case st.Weighted != nil:
			*seq++
			rVar := fmt.Sprintf("_w%d", *seq)
			b.WriteString(fmt.Sprintf("%sconst %s = Math.random() * 100;\n", indent, rVar))
			var totalW float64
			for _, br := range st.Weighted.Branches {
				totalW += br.Weight
			}
			if totalW <= 0 {
				totalW = 1
			}
			var cum float64
			for i, br := range st.Weighted.Branches {
				cum += br.Weight / totalW * 100
				if i == 0 {
					b.WriteString(fmt.Sprintf("%sif (%s < %.2f) {\n", indent, rVar, cum))
				} else if i == len(st.Weighted.Branches)-1 {
					b.WriteString(fmt.Sprintf("%s} else {\n", indent))
				} else {
					b.WriteString(fmt.Sprintf("%s} else if (%s < %.2f) {\n", indent, rVar, cum))
				}
				writeK6Steps(b, br.Steps, data, envVars, indent+"  ", seq)
			}
			b.WriteString(fmt.Sprintf("%s}\n", indent))
			writeK6Sleep(b, indent, st.Think)

		case st.Request != nil:
			writeK6HTTPRequest(b, &st, dataSet, envVars, indent, seq)
			writeK6Sleep(b, indent, st.Think)
		}
	}
}

func writeK6HTTPRequest(b *strings.Builder, st *scenario.Step, dataSet map[string]bool, envVars map[string]string, indent string, seq *int) {
	*seq++
	resVar := fmt.Sprintf("res_%d", *seq)
	req := st.Request
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}

	rawURL := req.URL
	var queryParts []string
	for _, q := range req.QueryList {
		queryParts = append(queryParts, fmt.Sprintf("%s=%s", q.Name, string(q.Value)))
	}
	if len(queryParts) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + strings.Join(queryParts, "&")
	}

	b.WriteString(fmt.Sprintf("%s// %s (%s)\n", indent, firstString(st.Name, st.ID), st.ID))
	if req.OAuth2 != nil {
		b.WriteString(fmt.Sprintf("%sconst _tok_%d = getOAuth2Token(%s, %s, %s, %s, '%s');\n",
			indent, *seq,
			toK6TemplateExpr(req.OAuth2.TokenURL, dataSet, envVars),
			toK6TemplateExpr(req.OAuth2.ClientID, dataSet, envVars),
			toK6TemplateExpr(req.OAuth2.ClientSecret, dataSet, envVars),
			toK6TemplateExpr(req.OAuth2.Scope, dataSet, envVars),
			jsEscape(req.OAuth2.ClientAuth)))
	}

	hdrEntries := make([]string, 0, len(req.Headers)+len(req.HeaderList)+2)
	hasCT := false
	for _, h := range req.HeaderList {
		if strings.EqualFold(h.Name, "Content-Type") {
			hasCT = true
		}
		hdrEntries = append(hdrEntries, fmt.Sprintf("'%s': %s", jsEscape(h.Name), toK6TemplateExpr(string(h.Value), dataSet, envVars)))
	}
	for k, v := range req.Headers {
		if strings.EqualFold(k, "Content-Type") {
			hasCT = true
		}
		hdrEntries = append(hdrEntries, fmt.Sprintf("'%s': %s", jsEscape(k), toK6TemplateExpr(v, dataSet, envVars)))
	}
	if req.OAuth2 != nil {
		hdrEntries = append(hdrEntries, fmt.Sprintf("'Authorization': `Bearer ${_tok_%d}`", *seq))
	}

	bodyExpr := "null"
	if req.Body != nil {
		switch req.Body.Type {
		case "json":
			if !hasCT {
				hdrEntries = append(hdrEntries, "'Content-Type': 'application/json'")
			}
			bodyExpr = toK6TemplateExpr(req.Body.Content, dataSet, envVars)
		case "raw":
			if req.Body.ContentType != "" && !hasCT {
				hdrEntries = append(hdrEntries, fmt.Sprintf("'Content-Type': '%s'", jsEscape(req.Body.ContentType)))
			}
			bodyExpr = toK6TemplateExpr(req.Body.Content, dataSet, envVars)
		case "form":
			var pairs []string
			for _, f := range req.Body.FieldList {
				pairs = append(pairs, fmt.Sprintf("'%s': %s", jsEscape(f.Name), toK6TemplateExpr(string(f.Value), dataSet, envVars)))
			}
			for k, v := range req.Body.Fields {
				pairs = append(pairs, fmt.Sprintf("'%s': %s", jsEscape(k), toK6TemplateExpr(string(v), dataSet, envVars)))
			}
			if len(pairs) > 0 {
				bodyExpr = "{ " + strings.Join(pairs, ", ") + " }"
			} else if req.Body.Content != "" {
				bodyExpr = toK6TemplateExpr(req.Body.Content, dataSet, envVars)
			}
		}
	}

	paramsExpr := fmt.Sprintf("{ tags: { name: '%s' }", jsEscape(firstString(st.Name, st.ID)))
	if len(hdrEntries) > 0 {
		paramsExpr += ", headers: { " + strings.Join(hdrEntries, ", ") + " }"
	}
	paramsExpr += " }"

	b.WriteString(fmt.Sprintf("%sconst %s = http.request('%s', %s, %s, %s);\n",
		indent, resVar, method, toK6TemplateExpr(rawURL, dataSet, envVars), bodyExpr, paramsExpr))

	for _, ex := range st.Extract {
		if ex.Disabled {
			continue
		}
		switch ex.From {
		case "jsonpath":
			jp := strings.TrimPrefix(strings.TrimPrefix(ex.Expr, "$."), "$")
			b.WriteString(fmt.Sprintf("%svars['%s'] = %s.json('%s');\n", indent, jsEscape(ex.Name), resVar, jsEscape(jp)))
		case "header":
			b.WriteString(fmt.Sprintf("%svars['%s'] = %s.headers['%s'];\n", indent, jsEscape(ex.Name), resVar, jsEscape(ex.Expr)))
		case "regex":
			b.WriteString(fmt.Sprintf("%svars['%s'] = (%s.body.match(/%s/) || [])[1] || '';\n", indent, jsEscape(ex.Name), resVar, ex.Expr))
		}
	}

	var checks []string
	for _, as := range st.Assert {
		if as.Disabled {
			continue
		}
		switch as.Type {
		case "status":
			if as.Equals != nil {
				checks = append(checks, fmt.Sprintf("'%s status %s': (r) => r.status === %s", jsEscape(st.ID), string(*as.Equals), string(*as.Equals)))
			}
		case "body_contains":
			checks = append(checks, fmt.Sprintf("'%s contains': (r) => r.body && r.body.includes('%s')", jsEscape(st.ID), jsEscape(as.Value)))
		case "duration":
			checks = append(checks, fmt.Sprintf("'%s duration <= %dms': (r) => r.timings.duration <= %d", jsEscape(st.ID), as.Max.D().Milliseconds(), as.Max.D().Milliseconds()))
		case "jsonpath":
			jp := strings.TrimPrefix(strings.TrimPrefix(as.Expr, "$."), "$")
			if as.Equals != nil {
				checks = append(checks, fmt.Sprintf("'%s jsonpath': (r) => String(r.json('%s')) === '%s'", jsEscape(st.ID), jsEscape(jp), jsEscape(string(*as.Equals))))
			}
		}
	}
	if len(checks) > 0 {
		b.WriteString(fmt.Sprintf("%scheck(%s, { %s });\n", indent, resVar, strings.Join(checks, ", ")))
	}
}

func writeK6Sleep(b *strings.Builder, indent string, th *scenario.Think) {
	if th == nil {
		return
	}
	d := th.Min.D()
	if d <= 0 {
		d = th.Mean.D()
	}
	if d <= 0 {
		d = th.Max.D()
	}
	if d > 0 {
		b.WriteString(fmt.Sprintf("%ssleep(%s);\n", indent, strconv.FormatFloat(d.Seconds(), 'f', 3, 64)))
	}
}

func toK6Threshold(th scenario.Threshold) (string, string) {
	op := th.Op
	if op == "" {
		op = "<="
		if th.Metric == "rps" {
			op = ">="
		}
	}
	val := strconv.FormatFloat(th.Value, 'f', -1, 64)
	m := strings.ToLower(strings.TrimSpace(th.Metric))
	tagSuffix := ""
	if th.Step != "" {
		tagSuffix = fmt.Sprintf("{name:%s}", th.Step)
	}
	switch m {
	case "p50", "p50_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("p(50)%s%s", op, val)
	case "p90", "p90_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("p(90)%s%s", op, val)
	case "p95", "p95_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("p(95)%s%s", op, val)
	case "p99", "p99_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("p(99)%s%s", op, val)
	case "max", "max_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("max%s%s", op, val)
	case "mean", "mean_ms", "avg", "avg_ms":
		return "http_req_duration" + tagSuffix, fmt.Sprintf("avg%s%s", op, val)
	case "error_rate":
		return "http_req_failed" + tagSuffix, fmt.Sprintf("rate%s%s", op, val)
	case "error_rate_pct", "error_pct":
		return "http_req_failed" + tagSuffix, fmt.Sprintf("rate%s%s", op, strconv.FormatFloat(th.Value/100, 'f', -1, 64))
	case "rps":
		return "http_reqs", fmt.Sprintf("rate%s%s", op, val)
	}
	return "", ""
}

func usesOAuth2InSteps(steps []scenario.Step) bool {
	for _, st := range steps {
		if st.Request != nil && st.Request.OAuth2 != nil {
			return true
		}
		if st.Group != nil && usesOAuth2InSteps(st.Group.Children) {
			return true
		}
		if st.Loop != nil && usesOAuth2InSteps(st.Loop.Children) {
			return true
		}
		if st.If != nil && (usesOAuth2InSteps(st.If.Then) || usesOAuth2InSteps(st.If.Else)) {
			return true
		}
		if st.Weighted != nil {
			for _, br := range st.Weighted.Branches {
				if usesOAuth2InSteps(br.Steps) {
					return true
				}
			}
		}
	}
	return false
}

func toK6TemplateExpr(raw string, dataSet map[string]bool, envVars map[string]string) string {
	if !strings.Contains(raw, k6EnvSentinel) && !strings.Contains(raw, "{{") {
		return "'" + jsEscape(raw) + "'"
	}
	s := jsTemplateEscape(raw)
	for strings.Contains(s, k6EnvSentinel) {
		idx := strings.Index(s, k6EnvSentinel)
		end := idx + len(k6EnvSentinel)
		for end < len(s) && ((s[end] >= 'A' && s[end] <= 'Z') || (s[end] >= '0' && s[end] <= '9') || s[end] == '_') {
			end++
		}
		envName := s[idx+len(k6EnvSentinel) : end]
		s = s[:idx] + "${__ENV." + envName + "}" + s[end:]
	}
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			break
		}
		closeIdx := strings.Index(s[open+2:], "}}")
		if closeIdx < 0 {
			break
		}
		key := strings.TrimSpace(s[open+2 : open+2+closeIdx])
		var repl string
		if envName, isSecret := envVars[key]; isSecret {
			repl = fmt.Sprintf("${__ENV.%s}", envName)
		} else if dot := strings.IndexByte(key, '.'); dot > 0 && dataSet[key[:dot]] {
			alias := sanitizeJSIdent(key[:dot])
			col := jsEscape(key[dot+1:])
			repl = fmt.Sprintf("${row_%s['%s'] || ''}", alias, col)
		} else {
			repl = fmt.Sprintf("${vars['%s'] || ''}", jsEscape(key))
		}
		s = s[:open] + repl + s[open+2+closeIdx+2:]
	}
	return "`" + s + "`"
}

func toK6Condition(expr string, dataSet map[string]bool, envVars map[string]string) string {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) == 2 {
		lhs := strings.Trim(parts[0], `"'`)
		lhsExpr := toK6TemplateExpr(lhs, dataSet, envVars)
		if parts[1] == "not_empty" {
			return fmt.Sprintf("Boolean(%s)", lhsExpr)
		}
		if parts[1] == "empty" {
			return fmt.Sprintf("!%s", lhsExpr)
		}
	}
	if len(parts) == 3 {
		lhs := toK6TemplateExpr(strings.Trim(parts[0], `"'`), dataSet, envVars)
		rhs := toK6TemplateExpr(strings.Trim(parts[2], `"'`), dataSet, envVars)
		op := parts[1]
		switch op {
		case "==", "!=", ">", "<", ">=", "<=":
			return fmt.Sprintf("%s %s %s", lhs, op, rhs)
		case "contains":
			return fmt.Sprintf("String(%s).includes(%s)", lhs, rhs)
		case "not_contains":
			return fmt.Sprintf("!String(%s).includes(%s)", lhs, rhs)
		}
	}
	return "true"
}

func k6Duration(d time.Duration) string {
	if d <= 0 {
		return "1s"
	}
	if d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return d.String()
}

func toK6EnvName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "SECRET"
	}
	return b.String()
}

func sanitizeJSIdent(s string) string {
	var b strings.Builder
	for i, r := range strings.TrimSpace(s) {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "item"
	}
	return b.String()
}

func jsEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}

func jsTemplateEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "`", "\\`", "${", `\${`)
	return r.Replace(s)
}
