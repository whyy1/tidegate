// Package exporter serializes Tidegate projects into OpenAPI 3.0 specifications
// and .tidegate zip archives, and renders standalone offline HTML load test reports.
package exporter

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/store"
)

// ExportOpenAPI exports a project to OpenAPI 3.0.3 JSON or YAML with secret
// fields scrubbed.
func ExportOpenAPI(st *store.Store, projectID string, asYAML bool) ([]byte, error) {
	pr, err := st.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(projectID)
	if err != nil {
		return nil, err
	}
	tree, err := st.Tree(projectID)
	if err != nil {
		return nil, err
	}

	info := map[string]any{
		"title":   pr.Name,
		"version": "1.0.0",
	}
	if pr.Description != "" {
		info["description"] = pr.Description
	}

	var servers []any
	for _, env := range envs {
		if strings.TrimSpace(env.BaseURL) == "" {
			continue
		}
		srvURL := convertDoubleBracesToSingle(env.BaseURL)
		srv := map[string]any{
			"url":         srvURL,
			"description": env.Name,
		}
		srvVars := map[string]any{}
		for _, v := range env.Variables {
			if v.Disabled || strings.TrimSpace(v.Name) == "" {
				continue
			}
			if strings.Contains(srvURL, "{"+v.Name+"}") {
				def := string(v.Value)
				if v.Secret {
					def = ""
				}
				entry := map[string]any{"default": def}
				if v.Description != "" {
					entry["description"] = v.Description
				}
				srvVars[v.Name] = entry
			}
		}
		if len(srvVars) > 0 {
			srv["variables"] = srvVars
		}
		servers = append(servers, srv)
	}

	paths := map[string]any{}
	secSchemes := map[string]any{}
	var tags []any
	seenTags := map[string]bool{}

	var walk func(folderSegs []string, inheritedAuth *store.Auth, nodes []*store.TreeNode) error
	walk = func(folderSegs []string, inheritedAuth *store.Auth, nodes []*store.TreeNode) error {
		for _, n := range nodes {
			if n.Type == store.NodeFolder {
				fld, err := st.GetFolder(projectID, n.ID)
				if err != nil {
					return err
				}
				nextSegs := append(append([]string(nil), folderSegs...), fld.Name)
				tagName := strings.Join(nextSegs, "/")
				if !seenTags[tagName] {
					seenTags[tagName] = true
					tags = append(tags, map[string]any{"name": tagName})
				}
				effAuth := inheritedAuth
				if fld.Auth != nil && fld.Auth.Type != "" && fld.Auth.Type != store.AuthInherit {
					effAuth = fld.Auth
				}
				if err := walk(nextSegs, effAuth, n.Children); err != nil {
					return err
				}
				continue
			}

			a, err := st.GetAPI(projectID, n.ID)
			if err != nil {
				return err
			}
			proto := strings.ToLower(strings.TrimSpace(a.Request.Protocol))
			if proto == "ws" || proto == "grpc" {
				continue
			}
			opPath := normalizeOpenAPIPath(a.Request.URL)
			method := strings.ToLower(strings.TrimSpace(a.Request.Method))
			if method == "" {
				method = "get"
			}

			op := map[string]any{
				"summary":   a.Name,
				"responses": openAPIResponses(a),
			}
			if a.Description != "" {
				op["description"] = a.Description
			}
			if len(folderSegs) > 0 {
				op["tags"] = []string{strings.Join(folderSegs, "/")}
			}

			var params []any
			seenPathParams := map[string]bool{}
			for _, p := range a.Request.PathParams {
				if p.Disabled || strings.TrimSpace(p.Name) == "" {
					continue
				}
				name := strings.TrimSpace(p.Name)
				seenPathParams[name] = true
				pm := map[string]any{
					"name":     name,
					"in":       "path",
					"required": true,
					"schema":   map[string]any{"type": "string"},
				}
				if !p.Secret && string(p.Value) != "" {
					pm["example"] = string(p.Value)
				}
				if p.Description != "" {
					pm["description"] = p.Description
				}
				params = append(params, pm)
			}
			// Ensure every {param} inside path has an OpenAPI path parameter declaration.
			for _, seg := range strings.Split(opPath, "/") {
				if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && !strings.HasPrefix(seg, "{{") {
					pname := seg[1 : len(seg)-1]
					if pname != "" && !seenPathParams[pname] {
						seenPathParams[pname] = true
						params = append(params, map[string]any{
							"name":     pname,
							"in":       "path",
							"required": true,
							"schema":   map[string]any{"type": "string"},
						})
					}
				}
			}
			for _, q := range a.Request.Query {
				if q.Disabled || strings.TrimSpace(q.Name) == "" {
					continue
				}
				qm := map[string]any{
					"name":   strings.TrimSpace(q.Name),
					"in":     "query",
					"schema": map[string]any{"type": "string"},
				}
				if !q.Secret && string(q.Value) != "" {
					qm["example"] = string(q.Value)
				}
				if q.Description != "" {
					qm["description"] = q.Description
				}
				params = append(params, qm)
			}
			for _, h := range a.Request.Headers {
				if h.Disabled || strings.TrimSpace(h.Name) == "" {
					continue
				}
				hm := map[string]any{
					"name":   strings.TrimSpace(h.Name),
					"in":     "header",
					"schema": map[string]any{"type": "string"},
				}
				if !h.Secret && string(h.Value) != "" && !isSensitiveHeaderName(h.Name) {
					hm["example"] = string(h.Value)
				}
				if h.Description != "" {
					hm["description"] = h.Description
				}
				params = append(params, hm)
			}
			if len(params) > 0 {
				op["parameters"] = params
			}

			if rb := openAPIRequestBody(a.Request.Body); rb != nil {
				op["requestBody"] = rb
			}

			effAuth := inheritedAuth
			if a.Auth != nil && a.Auth.Type != "" && a.Auth.Type != store.AuthInherit {
				effAuth = a.Auth
			}
			if effAuth != nil && effAuth.Type != "" && effAuth.Type != store.AuthNone && effAuth.Type != store.AuthInherit {
				schemeName := registerSecurityScheme(secSchemes, effAuth)
				if schemeName != "" {
					op["security"] = []any{map[string]any{schemeName: []string{}}}
				}
			}

			pathMap, _ := paths[opPath].(map[string]any)
			if pathMap == nil {
				pathMap = map[string]any{}
				paths[opPath] = pathMap
			}
			pathMap[method] = op
		}
		return nil
	}

	if err := walk(nil, nil, tree.Children); err != nil {
		return nil, err
	}

	doc := map[string]any{
		"openapi": "3.0.3",
		"info":    info,
		"paths":   paths,
	}
	if len(servers) > 0 {
		doc["servers"] = servers
	}
	if len(tags) > 0 {
		doc["tags"] = tags
	}
	if len(secSchemes) > 0 {
		doc["components"] = map[string]any{
			"securitySchemes": secSchemes,
		}
	}

	if asYAML {
		return yaml.Marshal(doc)
	}
	return json.MarshalIndent(doc, "", "  ")
}

func openAPIRequestBody(b *store.APIBody) map[string]any {
	if b == nil || b.Type == "" || b.Type == "none" {
		return nil
	}
	switch b.Type {
	case "json":
		media := map[string]any{
			"schema": map[string]any{"type": "object"},
		}
		var ex any
		if json.Unmarshal([]byte(b.Content), &ex) == nil {
			media["example"] = ex
		} else if b.Content != "" {
			media["example"] = b.Content
		}
		return map[string]any{
			"content": map[string]any{
				"application/json": media,
			},
		}
	case "form":
		return formBodySchema("application/x-www-form-urlencoded", b.Fields, nil)
	case "multipart":
		props := map[string]any{}
		for _, f := range b.Fields {
			if f.Disabled || strings.TrimSpace(f.Name) == "" {
				continue
			}
			prop := map[string]any{"type": "string"}
			if !f.Secret && string(f.Value) != "" {
				prop["example"] = string(f.Value)
			}
			if f.Description != "" {
				prop["description"] = f.Description
			}
			props[strings.TrimSpace(f.Name)] = prop
		}
		for _, bf := range b.Files {
			if bf.Disabled || strings.TrimSpace(bf.Field) == "" {
				continue
			}
			props[strings.TrimSpace(bf.Field)] = map[string]any{
				"type":   "string",
				"format": "binary",
			}
		}
		return map[string]any{
			"content": map[string]any{
				"multipart/form-data": map[string]any{
					"schema": map[string]any{"type": "object", "properties": props},
				},
			},
		}
	case "binary":
		ct := b.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		return map[string]any{
			"content": map[string]any{
				ct: map[string]any{
					"schema": map[string]any{"type": "string", "format": "binary"},
				},
			},
		}
	default:
		ct := b.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		media := map[string]any{
			"schema": map[string]any{"type": "string"},
		}
		if b.Content != "" {
			media["example"] = b.Content
		}
		return map[string]any{
			"content": map[string]any{ct: media},
		}
	}
}

func formBodySchema(mediaType string, fields store.KeyValueList, _ any) map[string]any {
	props := map[string]any{}
	for _, f := range fields {
		if f.Disabled || strings.TrimSpace(f.Name) == "" {
			continue
		}
		prop := map[string]any{"type": "string"}
		if !f.Secret && string(f.Value) != "" {
			prop["example"] = string(f.Value)
		}
		if f.Description != "" {
			prop["description"] = f.Description
		}
		props[strings.TrimSpace(f.Name)] = prop
	}
	return map[string]any{
		"content": map[string]any{
			mediaType: map[string]any{
				"schema": map[string]any{"type": "object", "properties": props},
			},
		},
	}
}

func registerSecurityScheme(schemes map[string]any, a *store.Auth) string {
	switch a.Type {
	case store.AuthBearer:
		schemes["bearerAuth"] = map[string]any{"type": "http", "scheme": "bearer"}
		return "bearerAuth"
	case store.AuthBasic:
		schemes["basicAuth"] = map[string]any{"type": "http", "scheme": "basic"}
		return "basicAuth"
	case store.AuthAPIKey:
		in := a.In
		if in != "query" {
			in = "header"
		}
		key := a.Key
		if key == "" {
			key = "X-API-Key"
		}
		name := "apiKey_" + in + "_" + sanitizeSchemeID(key)
		schemes[name] = map[string]any{"type": "apiKey", "in": in, "name": key}
		return name
	case store.AuthOAuth2:
		scopes := map[string]any{}
		for _, sc := range strings.Fields(a.Scope) {
			scopes[sc] = sc
		}
		tokURL := a.TokenURL
		if tokURL == "" {
			tokURL = "https://example.com/oauth/token"
		}
		schemes["oauth2ClientCredentials"] = map[string]any{
			"type": "oauth2",
			"flows": map[string]any{
				"clientCredentials": map[string]any{
					"tokenUrl": tokURL,
					"scopes":   scopes,
				},
			},
		}
		return "oauth2ClientCredentials"
	}
	return ""
}

func openAPIResponses(a *store.API) map[string]any {
	defaultMediaType := "application/json"
	if a != nil && strings.EqualFold(strings.TrimSpace(a.Request.Protocol), "sse") {
		defaultMediaType = "text/event-stream"
	}
	if a == nil || (len(a.Examples) == 0 && len(a.ResponseFields) == 0) {
		if defaultMediaType == "text/event-stream" {
			return map[string]any{
				"200": map[string]any{
					"description": "成功",
					"content": map[string]any{
						defaultMediaType: map[string]any{
							"schema": map[string]any{"type": "string"},
						},
					},
				},
			}
		}
		return map[string]any{
			"200": map[string]any{"description": "成功"},
		}
	}
	schema := buildSchemaFromResponseFields(a.ResponseFields)
	if len(a.Examples) == 0 {
		return map[string]any{
			"200": map[string]any{
				"description": "成功",
				"content": map[string]any{
					defaultMediaType: map[string]any{
						"schema": schema,
					},
				},
			},
		}
	}

	byStatus := map[string][]store.APIResponseExample{}
	for _, ex := range a.Examples {
		code := "200"
		if ex.Status > 0 {
			code = strconv.Itoa(ex.Status)
		}
		byStatus[code] = append(byStatus[code], ex)
	}
	out := map[string]any{}
	codes := make([]string, 0, len(byStatus))
	for code := range byStatus {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for idx, code := range codes {
		list := byStatus[code]
		content := map[string]any{}
		byMedia := map[string][]store.APIResponseExample{}
		for _, ex := range list {
			ct := strings.TrimSpace(ex.ContentType)
			if ct == "" {
				ct = defaultMediaType
			}
			byMedia[ct] = append(byMedia[ct], ex)
		}
		for ct, mList := range byMedia {
			media := map[string]any{}
			if len(schema) > 0 && (strings.HasPrefix(code, "2") || idx == 0) {
				media["schema"] = schema
			}
			if len(mList) == 1 {
				media["example"] = parseExampleValue(mList[0].Body)
			} else {
				exMap := map[string]any{}
				for j, ex := range mList {
					key := strings.TrimSpace(ex.Name)
					if key == "" {
						key = "example_" + strconv.Itoa(j+1)
					}
					exMap[key] = map[string]any{
						"summary": ex.Name,
						"value":   parseExampleValue(ex.Body),
					}
				}
				media["examples"] = exMap
			}
			content[ct] = media
		}
		respObj := map[string]any{
			"description": "响应示例 (" + code + ")",
			"content":     content,
		}
		if len(list[0].Headers) > 0 {
			hdrs := map[string]any{}
			for _, h := range list[0].Headers {
				if h.Disabled || strings.TrimSpace(h.Name) == "" || isSensitiveHeaderName(h.Name) {
					continue
				}
				hEntry := map[string]any{"schema": map[string]any{"type": "string"}}
				if h.Description != "" {
					hEntry["description"] = h.Description
				}
				hdrs[strings.TrimSpace(h.Name)] = hEntry
			}
			if len(hdrs) > 0 {
				respObj["headers"] = hdrs
			}
		}
		out[code] = respObj
	}
	return out
}

func parseExampleValue(body string) any {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	var parsed any
	if json.Unmarshal([]byte(trimmed), &parsed) == nil {
		return parsed
	}
	return body
}

func buildSchemaFromResponseFields(fields []store.APIFieldDoc) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	root := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	for _, f := range fields {
		path := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(f.Path, "$."), "$"))
		if path == "" {
			continue
		}
		segs := strings.Split(path, ".")
		insertFieldSchema(root, segs, f)
	}
	return root
}

func insertFieldSchema(obj map[string]any, segs []string, doc store.APIFieldDoc) {
	props, _ := obj["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		obj["properties"] = props
	}
	key := segs[0]
	if len(segs) == 1 {
		ftype := strings.ToLower(strings.TrimSpace(doc.Type))
		if ftype == "" {
			ftype = "string"
		}
		leaf := map[string]any{"type": ftype}
		if doc.Description != "" {
			leaf["description"] = doc.Description
		}
		props[key] = leaf
		if doc.Required {
			req, _ := obj["required"].([]string)
			req = append(req, key)
			obj["required"] = req
		}
		return
	}
	child, _ := props[key].(map[string]any)
	if child == nil {
		child = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
		props[key] = child
	}
	insertFieldSchema(child, segs[1:], doc)
}

func normalizeOpenAPIPath(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if idx := strings.IndexAny(s, "?#"); idx >= 0 {
		s = s[:idx]
	}
	if strings.HasPrefix(strings.ToLower(s), "http://") || strings.HasPrefix(strings.ToLower(s), "https://") {
		if idx := strings.Index(s, "://"); idx >= 0 {
			rest := s[idx+3:]
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				s = rest[slash:]
			} else {
				s = "/"
			}
		}
	}
	if strings.HasPrefix(s, "{{") {
		if end := strings.Index(s, "}}"); end >= 0 {
			s = s[end+2:]
		}
	}
	if s == "" {
		return "/"
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	parts := strings.Split(s, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			parts[i] = "{" + seg[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func convertDoubleBracesToSingle(s string) string {
	return strings.NewReplacer("{{", "{", "}}", "}").Replace(s)
}

func sanitizeSchemeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isSensitiveHeaderName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-auth-token", "x-access-token", "x-csrf-token":
		return true
	}
	return false
}
