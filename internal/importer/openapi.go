package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

const maxRefDepth = 8

var openAPIMethods = []string{"get", "post", "put", "delete", "patch", "head", "options"}

func parseOpenAPIOrSwagger(content []byte, hint string) (*parsedBundle, error) {
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, fmt.Errorf("解析 OpenAPI / Swagger 文档失败：%w", err)
	}
	if len(root) == 0 {
		return nil, errors.New("OpenAPI / Swagger 文档为空")
	}

	report := &ConversionReport{}
	isSwagger2 := false
	if v := asString(root["swagger"]); v != "" {
		isSwagger2 = true
		report.Format = FormatSwagger2
		report.DetectedVersion = v
	} else if v := asString(root["openapi"]); v != "" {
		report.Format = FormatOpenAPI3
		report.DetectedVersion = v
	} else if hint == FormatSwagger2 {
		isSwagger2 = true
		report.Format = FormatSwagger2
	} else {
		report.Format = FormatOpenAPI3
	}

	b := &parsedBundle{
		ProjectName: asString(asMap(root["info"])["title"]),
		ProjectDesc: asString(asMap(root["info"])["description"]),
		Report:      report,
	}

	if isSwagger2 {
		parseSwagger2Meta(root, b)
	} else {
		parseOpenAPI3Servers(root, b)
	}

	schemes := collectSecuritySchemes(root, isSwagger2)
	rootSec := asSlice(root["security"])

	// Declare folders from top-level tags in order.
	declaredTags := map[string]bool{}
	for idx, rawTag := range asSlice(root["tags"]) {
		tm := asMap(rawTag)
		name := strings.TrimSpace(asString(tm["name"]))
		if name == "" || declaredTags[name] {
			continue
		}
		declaredTags[name] = true
		b.Folders = append(b.Folders, parsedFolder{
			Path:  []string{name},
			Order: int64((idx + 1) * 1024),
		})
	}

	pathsMap := asMap(root["paths"])
	pathKeys := sortedMapKeys(pathsMap)
	order := int64(1024)

	for _, rawPath := range pathKeys {
		pathItem := resolveObj(root, asMap(pathsMap[rawPath]), 0, map[string]bool{}, report)
		if len(pathItem) == 0 {
			continue
		}
		pathParams := collectParameters(root, asSlice(pathItem["parameters"]), report)

		for _, m := range openAPIMethods {
			opRaw, ok := pathItem[m]
			if !ok {
				continue
			}
			op := resolveObj(root, asMap(opRaw), 0, map[string]bool{}, report)
			if len(op) == 0 {
				continue
			}

			method := strings.ToUpper(m)
			opParams := mergeParameters(pathParams, collectParameters(root, asSlice(op["parameters"]), report))

			name := strings.TrimSpace(asString(op["summary"]))
			if name == "" {
				name = strings.TrimSpace(asString(op["operationId"]))
			}
			if name == "" {
				name = method + " " + rawPath
			}

			var folderPath []string
			for _, rt := range asSlice(op["tags"]) {
				if t := strings.TrimSpace(asString(rt)); t != "" {
					folderPath = []string{t}
					break
				}
			}

			req := store.APIRequest{
				Method: method,
				URL:    rawPath,
			}

			var bodyForm store.KeyValueList
			var bodyMultiFields store.KeyValueList
			var bodyMultiFiles []scenario.BodyFile
			var bodyContent string
			var bodyType string
			var bodyContentType string

			for _, p := range opParams {
				pIn := strings.ToLower(asString(p["in"]))
				pName := strings.TrimSpace(asString(p["name"]))
				if pName == "" {
					continue
				}
				pDesc := asString(p["description"])
				val := paramSampleValue(root, p, report)

				switch pIn {
				case "path":
					req.PathParams = append(req.PathParams, store.KeyValue{Name: pName, Value: scenario.Scalar(val), Description: pDesc})
				case "query":
					req.Query = append(req.Query, store.KeyValue{Name: pName, Value: scenario.Scalar(val), Description: pDesc})
				case "header":
					req.Headers = append(req.Headers, store.KeyValue{Name: pName, Value: scenario.Scalar(val), Description: pDesc})
				case "cookie":
					report.Skipped = append(report.Skipped, fmt.Sprintf("%s %s：Cookie 参数 %q 已跳过（建议在 Header 或会话 Cookie 中设置）", method, rawPath, pName))
				case "formdata":
					if isSwagger2 {
						if strings.EqualFold(asString(p["type"]), "file") {
							bodyType = "multipart"
							bodyMultiFiles = append(bodyMultiFiles, scenario.BodyFile{Field: pName, Path: pName + ".bin"})
						} else {
							if bodyType == "" {
								bodyType = "form"
							}
							kv := store.KeyValue{Name: pName, Value: scenario.Scalar(val), Description: pDesc}
							bodyForm = append(bodyForm, kv)
							bodyMultiFields = append(bodyMultiFields, kv)
						}
					}
				case "body":
					if isSwagger2 && bodyType == "" {
						bodyType = "json"
						ex := exampleFromSchema(root, resolveObj(root, asMap(p["schema"]), 0, map[string]bool{}, report), 0, map[string]bool{})
						bodyContent = marshalPrettyExample(ex)
					}
				}
			}

			if !isSwagger2 {
				if rbRaw, hasRB := op["requestBody"]; hasRB {
					rb := resolveObj(root, asMap(rbRaw), 0, map[string]bool{}, report)
					contentMap := asMap(rb["content"])
					bt, bc, bf, bmf, bct := parseOpenAPI3RequestBody(root, contentMap, report)
					if bt != "" {
						bodyType, bodyContent, bodyForm, bodyMultiFiles, bodyContentType = bt, bc, bf, bmf, bct
						if bt == "multipart" {
							bodyMultiFields = bf
						}
					}
				}
			}

			switch bodyType {
			case "json":
				req.Body = &store.APIBody{Type: "json", Content: bodyContent}
			case "form":
				req.Body = &store.APIBody{Type: "form", Fields: bodyForm}
			case "multipart":
				req.Body = &store.APIBody{Type: "multipart", Fields: bodyMultiFields, Files: bodyMultiFiles}
			case "raw":
				req.Body = &store.APIBody{Type: "raw", Content: bodyContent, ContentType: bodyContentType}
			case "binary":
				req.Body = &store.APIBody{Type: "binary", ContentType: bodyContentType}
			}

			secList := rootSec
			if opSec, has := op["security"]; has {
				secList = asSlice(opSec)
			}
			auth := resolveOperationAuth(secList, schemes, method+" "+rawPath, report)

			api := store.API{
				Name:        name,
				Order:       order,
				Description: strings.TrimSpace(asString(op["description"])),
				Request:     req,
				Auth:        auth,
			}
			order += 1024
			b.APIs = append(b.APIs, parsedAPI{
				FolderPath: folderPath,
				API:        api,
			})
		}
	}

	b.Report.APIsFound = len(b.APIs)
	return b, nil
}

func parseOpenAPI3Servers(root map[string]any, b *parsedBundle) {
	servers := asSlice(root["servers"])
	for idx, raw := range servers {
		sm := asMap(raw)
		rawURL := strings.TrimSpace(asString(sm["url"]))
		if rawURL == "" {
			continue
		}
		var vars store.KeyValueList
		varsMap := asMap(sm["variables"])
		for _, k := range sortedMapKeys(varsMap) {
			vm := asMap(varsMap[k])
			def := asString(vm["default"])
			desc := asString(vm["description"])
			vars = append(vars, store.KeyValue{
				Name:        k,
				Value:       scenario.Scalar(def),
				Description: desc,
			})
			rawURL = strings.ReplaceAll(rawURL, "{"+k+"}", "{{"+k+"}}")
		}
		envName := strings.TrimSpace(asString(sm["description"]))
		if envName == "" {
			if idx == 0 {
				envName = "默认环境"
			} else {
				envName = fmt.Sprintf("环境 %d", idx+1)
			}
		}
		b.Envs = append(b.Envs, parsedEnv{
			Env: store.Environment{
				Name:      envName,
				BaseURL:   strings.TrimRight(rawURL, "/"),
				Variables: vars,
			},
		})
	}
}

func parseSwagger2Meta(root map[string]any, b *parsedBundle) {
	host := strings.TrimSpace(asString(root["host"]))
	if host == "" {
		return
	}
	scheme := "https"
	if ss := asSlice(root["schemes"]); len(ss) > 0 {
		if s := strings.TrimSpace(asString(ss[0])); s != "" {
			scheme = s
		}
	}
	basePath := strings.TrimSpace(asString(root["basePath"]))
	if basePath == "/" {
		basePath = ""
	}
	baseURL := scheme + "://" + host + basePath
	b.Envs = append(b.Envs, parsedEnv{
		Env: store.Environment{
			Name:    "默认环境",
			BaseURL: strings.TrimRight(baseURL, "/"),
		},
	})
}

func parseOpenAPI3RequestBody(root, content map[string]any, report *ConversionReport) (bodyType, bodyContent string, fields store.KeyValueList, files []scenario.BodyFile, contentType string) {
	if len(content) == 0 {
		return "", "", nil, nil, ""
	}
	// Prefer JSON, then form, multipart, text/raw.
	ctKeys := sortedMapKeys(content)
	picked := ctKeys[0]
	for _, pref := range []string{"application/json", "application/x-www-form-urlencoded", "multipart/form-data"} {
		for _, k := range ctKeys {
			if strings.Contains(strings.ToLower(k), pref) {
				picked = k
				goto selected
			}
		}
	}
selected:
	media := asMap(content[picked])
	schema := resolveObj(root, asMap(media["schema"]), 0, map[string]bool{}, report)
	low := strings.ToLower(picked)
	switch {
	case strings.Contains(low, "json"):
		ex := media["example"]
		if ex == nil {
			exs := asMap(media["examples"])
			for _, k := range sortedMapKeys(exs) {
				exObj := resolveObj(root, asMap(exs[k]), 0, map[string]bool{}, report)
				if v, ok := exObj["value"]; ok {
					ex = v
					break
				}
			}
		}
		if ex == nil && len(schema) > 0 {
			ex = exampleFromSchema(root, schema, 0, map[string]bool{})
		}
		return "json", marshalPrettyExample(ex), nil, nil, ""
	case strings.Contains(low, "application/x-www-form-urlencoded"):
		props := asMap(schema["properties"])
		for _, k := range sortedMapKeys(props) {
			pm := resolveObj(root, asMap(props[k]), 0, map[string]bool{}, report)
			v := scalarString(exampleFromSchema(root, pm, 0, map[string]bool{}))
			fields = append(fields, store.KeyValue{Name: k, Value: scenario.Scalar(v), Description: asString(pm["description"])})
		}
		return "form", "", fields, nil, ""
	case strings.Contains(low, "multipart/form-data"):
		props := asMap(schema["properties"])
		for _, k := range sortedMapKeys(props) {
			pm := resolveObj(root, asMap(props[k]), 0, map[string]bool{}, report)
			if strings.EqualFold(asString(pm["format"]), "binary") {
				files = append(files, scenario.BodyFile{Field: k, Path: k + ".bin"})
			} else {
				v := scalarString(exampleFromSchema(root, pm, 0, map[string]bool{}))
				fields = append(fields, store.KeyValue{Name: k, Value: scenario.Scalar(v), Description: asString(pm["description"])})
			}
		}
		return "multipart", "", fields, files, ""
	case strings.Contains(low, "octet-stream"):
		return "binary", "", nil, nil, picked
	default:
		ex := media["example"]
		if ex == nil && len(schema) > 0 {
			ex = exampleFromSchema(root, schema, 0, map[string]bool{})
		}
		return "raw", scalarString(ex), nil, nil, picked
	}
}

type secScheme struct {
	Type   string
	Scheme string
	In     string
	Name   string
}

func collectSecuritySchemes(root map[string]any, swagger2 bool) map[string]secScheme {
	out := map[string]secScheme{}
	var raw map[string]any
	if swagger2 {
		raw = asMap(root["securityDefinitions"])
	} else {
		raw = asMap(asMap(root["components"])["securitySchemes"])
	}
	for k, v := range raw {
		m := asMap(v)
		out[k] = secScheme{
			Type:   strings.ToLower(asString(m["type"])),
			Scheme: strings.ToLower(asString(m["scheme"])),
			In:     strings.ToLower(asString(m["in"])),
			Name:   asString(m["name"]),
		}
	}
	return out
}

func resolveOperationAuth(sec []any, schemes map[string]secScheme, label string, report *ConversionReport) *store.Auth {
	if len(sec) == 0 {
		return nil
	}
	for _, entry := range sec {
		m := asMap(entry)
		if len(m) == 0 {
			return &store.Auth{Type: store.AuthNone}
		}
		for _, k := range sortedMapKeys(m) {
			sc, ok := schemes[k]
			if !ok {
				continue
			}
			switch sc.Type {
			case "http":
				switch sc.Scheme {
				case "bearer":
					return &store.Auth{Type: store.AuthBearer, Token: "{{token}}"}
				case "basic":
					return &store.Auth{Type: store.AuthBasic, Username: "{{username}}", Password: "{{password}}"}
				}
			case "basic":
				return &store.Auth{Type: store.AuthBasic, Username: "{{username}}", Password: "{{password}}"}
			case "apikey":
				in := sc.In
				if in != "query" {
					in = "header"
				}
				if in == "cookie" {
					report.Skipped = append(report.Skipped, fmt.Sprintf("%s：Cookie 类型安全方案 %q 已跳过", label, k))
					continue
				}
				return &store.Auth{Type: store.AuthAPIKey, Key: sc.Name, Value: "{{api_key}}", In: in}
			case "oauth2", "openidconnect":
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s：认证类型 %s (%s) 暂不支持自动配置，请手动填写鉴权", label, sc.Type, k))
			}
		}
	}
	return nil
}

func collectParameters(root map[string]any, list []any, report *ConversionReport) []map[string]any {
	var out []map[string]any
	for _, item := range list {
		m := resolveObj(root, asMap(item), 0, map[string]bool{}, report)
		if len(m) > 0 {
			out = append(out, m)
		}
	}
	return out
}

func mergeParameters(base, extra []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(base)+len(extra))
	pos := map[string]int{}
	keyOf := func(m map[string]any) string {
		return strings.ToLower(asString(m["in"])) + ":" + asString(m["name"])
	}
	for _, m := range base {
		k := keyOf(m)
		pos[k] = len(out)
		out = append(out, m)
	}
	for _, m := range extra {
		k := keyOf(m)
		if idx, exists := pos[k]; exists {
			out[idx] = m
		} else {
			pos[k] = len(out)
			out = append(out, m)
		}
	}
	return out
}

func paramSampleValue(root, p map[string]any, report *ConversionReport) string {
	if v, ok := p["example"]; ok {
		return scalarString(v)
	}
	if v, ok := p["default"]; ok {
		return scalarString(v)
	}
	if sc := asMap(p["schema"]); len(sc) > 0 {
		ex := exampleFromSchema(root, resolveObj(root, sc, 0, map[string]bool{}, report), 0, map[string]bool{})
		if ex != nil {
			return scalarString(ex)
		}
	}
	return ""
}

func resolveObj(root, node map[string]any, depth int, seen map[string]bool, report *ConversionReport) map[string]any {
	if len(node) == 0 {
		return nil
	}
	ref := strings.TrimSpace(asString(node["$ref"]))
	if ref == "" {
		return node
	}
	if depth >= maxRefDepth || seen[ref] {
		if report != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("检测到过深或循环 $ref 引用 %q，已停止展开", ref))
		}
		return node
	}
	target := lookupJSONPointer(root, ref)
	if target == nil {
		if report != nil && !strings.HasPrefix(ref, "#/") {
			report.Skipped = append(report.Skipped, fmt.Sprintf("外部 $ref 引用 %q 已跳过", ref))
		}
		return node
	}
	nextSeen := make(map[string]bool, len(seen)+1)
	for k, v := range seen {
		nextSeen[k] = v
	}
	nextSeen[ref] = true

	resolved := resolveObj(root, target, depth+1, nextSeen, report)
	if len(node) == 1 {
		return resolved
	}
	merged := make(map[string]any, len(resolved)+len(node))
	for k, v := range resolved {
		merged[k] = v
	}
	for k, v := range node {
		if k != "$ref" {
			merged[k] = v
		}
	}
	return merged
}

func lookupJSONPointer(root map[string]any, ptr string) map[string]any {
	if !strings.HasPrefix(ptr, "#/") {
		return nil
	}
	parts := strings.Split(ptr[2:], "/")
	var cur any = root
	for _, seg := range parts {
		unescaped := strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m := asMap(cur)
		if m == nil {
			return nil
		}
		v, ok := m[unescaped]
		if !ok {
			return nil
		}
		cur = v
	}
	return asMap(cur)
}

func exampleFromSchema(root, schema map[string]any, depth int, seen map[string]bool) any {
	if len(schema) == 0 || depth > 5 {
		return nil
	}
	if ref := strings.TrimSpace(asString(schema["$ref"])); ref != "" {
		if seen[ref] {
			return nil
		}
		nextSeen := make(map[string]bool, len(seen)+1)
		for k, v := range seen {
			nextSeen[k] = v
		}
		nextSeen[ref] = true
		if target := lookupJSONPointer(root, ref); target != nil {
			return exampleFromSchema(root, target, depth+1, nextSeen)
		}
	}
	if v, ok := schema["example"]; ok {
		return v
	}
	if v, ok := schema["default"]; ok {
		return v
	}
	if enums := asSlice(schema["enum"]); len(enums) > 0 {
		return enums[0]
	}
	if allOf := asSlice(schema["allOf"]); len(allOf) > 0 {
		merged := map[string]any{}
		for _, sub := range allOf {
			v := exampleFromSchema(root, asMap(sub), depth+1, seen)
			for k, val := range asMap(v) {
				merged[k] = val
			}
		}
		return merged
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if alts := asSlice(schema[key]); len(alts) > 0 {
			for _, sub := range alts {
				if m := asMap(sub); len(m) > 0 && asString(m["type"]) != "null" {
					return exampleFromSchema(root, m, depth+1, seen)
				}
			}
		}
	}

	typ := strings.ToLower(asString(schema["type"]))
	if typ == "" && len(asMap(schema["properties"])) > 0 {
		typ = "object"
	}
	switch typ {
	case "object":
		props := asMap(schema["properties"])
		obj := make(map[string]any, len(props))
		for _, k := range sortedMapKeys(props) {
			obj[k] = exampleFromSchema(root, asMap(props[k]), depth+1, seen)
		}
		return obj
	case "array":
		item := exampleFromSchema(root, asMap(schema["items"]), depth+1, seen)
		if item == nil {
			return []any{}
		}
		return []any{item}
	case "integer":
		return 0
	case "number":
		return 0.0
	case "boolean":
		return false
	case "string":
		switch strings.ToLower(asString(schema["format"])) {
		case "uuid":
			return "00000000-0000-0000-0000-000000000000"
		case "email":
			return "user@example.com"
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "date":
			return "2026-01-01"
		default:
			return "string"
		}
	}
	return nil
}

func marshalPrettyExample(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out
	}
	return nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return asString(v)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
