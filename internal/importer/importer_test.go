package importer

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name    string
		content string
		file    string
		want    string
	}{
		{"openapi3_json", `{"openapi":"3.0.3","paths":{}}`, "", FormatOpenAPI3},
		{"openapi3_yaml", "openapi: 3.1.0\ninfo:\n  title: Demo\npaths: {}\n", "spec.yaml", FormatOpenAPI3},
		{"swagger2", "swagger: \"2.0\"\ninfo:\n  title: Legacy\npaths: {}\n", "", FormatSwagger2},
		{"postman_col", `{"info":{"name":"Demo","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[]}`, "", FormatPostman},
		{"postman_env", `{"name":"Prod","_postman_variable_scope":"environment","values":[]}`, "", FormatPostman},
		{"har", `{"log":{"version":"1.2","entries":[]}}`, "cap.har", FormatHAR},
		{"curl", "$ curl https://example.com/ping", "", FormatCurl},
		{"tidegate", "PK\x03\x04dummy", "proj.tidegate", FormatTidegate},
	}
	for _, tc := range cases {
		got, err := DetectFormat([]byte(tc.content), tc.file)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got (%q, %v), want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestOpenAPI3TwoStepPreviewApplyPreservesULID(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(&store.Project{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	usersFolder, err := st.CreateFolder(pr.ID, &store.Folder{Name: "Users"})
	if err != nil {
		t.Fatal(err)
	}
	existingAPI, err := st.CreateAPI(pr.ID, &store.API{
		Name:     "old-get-user",
		FolderID: usersFolder.ID,
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/users/:id",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scen, err := st.CreateScenario(pr.ID, &store.Scenario{
		Name: "smoke",
		Steps: []store.ScenarioStep{
			{ID: "s1", APIID: existingAPI.ID},
		},
		Load: scenario.Load{
			Stages: []scenario.Stage{{Duration: scenario.Duration(time.Second), Target: 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := `
openapi: 3.0.3
info:
  title: Users Service
servers:
  - url: https://{region}.api.example.com/v1
    description: Prod
    variables:
      region:
        default: cn
tags:
  - name: Users
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
    oauth2Auth:
      type: oauth2
  schemas:
    Cyclic:
      type: object
      properties:
        self:
          $ref: '#/components/schemas/Cyclic'
    CreateUser:
      type: object
      properties:
        name:
          type: string
          example: alice
        cycle:
          $ref: '#/components/schemas/Cyclic'
paths:
  /users/{id}:
    get:
      tags: [Users]
      summary: 获取用户详情
      security:
        - bearerAuth: []
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            example: "42"
      responses:
        "200":
          description: ok
  /users:
    post:
      tags: [Users]
      summary: 创建用户
      security:
        - oauth2Auth: []
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/CreateUser'
      responses:
        "201":
          description: created
`

	// Step 1: Preview must not modify store!
	pv, err := Preview(st, pr.ID, PreviewInput{
		Content:  []byte(spec),
		Strategy: StrategyMerge,
	})
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if pv.Report.Format != FormatOpenAPI3 || pv.Report.APIsFound != 2 || len(pv.Diffs) != 2 {
		t.Fatalf("unexpected preview report/diffs: %+v %+v", pv.Report, pv.Diffs)
	}
	if len(pv.Report.Skipped) == 0 {
		t.Fatalf("expected skipped oauth2 report, got %+v", pv.Report)
	}
	afterPreview, _ := st.GetAPI(pr.ID, existingAPI.ID)
	if afterPreview.Name != "old-get-user" {
		t.Fatal("Preview mutated existing API")
	}

	// Step 2a: Apply with StrategySkip keeps existing API unchanged.
	skipRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(spec), Strategy: StrategySkip})
	if err != nil {
		t.Fatal(err)
	}
	if skipRes.Report.APIsCreated != 1 || skipRes.Report.APIsSkipped != 1 || skipRes.Report.EnvsCreated != 1 {
		t.Fatalf("unexpected skip counts: %+v", skipRes.Report)
	}
	stillOld, _ := st.GetAPI(pr.ID, existingAPI.ID)
	if stillOld.Name != "old-get-user" {
		t.Fatal("StrategySkip mutated matched API")
	}

	// Step 2b: Apply with StrategyMerge updates existing API preserving its ULID!
	mergeRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(spec), Strategy: StrategyMerge})
	if err != nil {
		t.Fatal(err)
	}
	if mergeRes.Report.APIsUpdated != 2 {
		t.Fatalf("unexpected merge counts: %+v", mergeRes.Report)
	}
	updated, err := st.GetAPI(pr.ID, existingAPI.ID)
	if err != nil {
		t.Fatalf("existing API ULID disappeared: %v", err)
	}
	if updated.Name != "获取用户详情" || updated.Auth == nil || updated.Auth.Type != store.AuthBearer {
		t.Fatalf("updated API mismatch: %+v", updated)
	}

	// Scenario referencing original API ULID still resolves cleanly with the imported environment!
	envs, err := st.ListEnvironments(pr.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("expected 1 imported env, got %v err=%v", envs, err)
	}
	resolved, err := st.ResolveScenario(pr.ID, scen.ID, store.ResolveOptions{
		EnvironmentID: envs[0].ID,
		Variables:     map[string]scenario.Scalar{"token": "demo-tok"},
	})
	if err != nil || len(resolved.Definition.Steps) != 1 || resolved.Definition.Steps[0].Request.URL != "https://{{region}}.api.example.com/v1/users/42" {
		t.Fatalf("scenario resolution broke after import update: err=%v resolved=%+v", err, resolved)
	}
	if _, err := resolved.Compile(); err != nil {
		t.Fatalf("resolved scenario failed compile: %v", err)
	}
}

func TestSwagger2PostmanHARAndCurl(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	pr, _ := st.CreateProject(&store.Project{Name: "formats"})

	// Swagger 2.0
	sw := `{
	  "swagger": "2.0",
	  "host": "legacy.example.com",
	  "basePath": "/api",
	  "schemes": ["https"],
	  "securityDefinitions": {"keyAuth": {"type": "apiKey", "in": "header", "name": "X-Key"}},
	  "definitions": {"Item": {"type": "object", "properties": {"id": {"type": "integer"}}}},
	  "paths": {
	    "/items": {
	      "post": {
	        "summary": "add-item",
	        "security": [{"keyAuth": []}],
	        "parameters": [{"in": "body", "name": "body", "schema": {"$ref": "#/definitions/Item"}}],
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	swRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(sw)})
	if err != nil || swRes.Report.Format != FormatSwagger2 || swRes.Report.APIsCreated != 1 || swRes.Report.EnvsCreated != 1 {
		t.Fatalf("swagger2 apply = %+v err=%v", swRes, err)
	}

	// Postman Collection + Environment
	pmCol := `{
	  "info": {"name": "Orders", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
	  "variable": [{"key": "host", "value": "https://orders.example.com"}],
	  "auth": {"type": "bearer", "bearer": [{"key": "token", "value": "{{tk}}"}]},
	  "item": [
	    {
	      "name": "Auth",
	      "event": [{"listen": "test"}],
	      "item": [
	        {
	          "name": "Get Order",
	          "request": {
	            "method": "GET",
	            "url": {
	              "raw": "{{host}}/orders/:oid?expand=items",
	              "host": ["{{host}}"],
	              "path": ["orders", ":oid"],
	              "query": [{"key": "expand", "value": "items", "disabled": false}],
	              "variable": [{"key": "oid", "value": "100"}]
	            },
	            "header": [{"key": "X-Debug", "value": "1", "disabled": true}]
	          }
	        }
	      ]
	    }
	  ]
	}`
	pmRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(pmCol)})
	if err != nil || pmRes.Report.Format != FormatPostman || pmRes.Report.APIsCreated != 1 || len(pmRes.Report.Skipped) == 0 {
		t.Fatalf("postman apply = %+v err=%v", pmRes, err)
	}

	pmEnv := `{"name":"Staging","_postman_variable_scope":"environment","values":[{"key":"base_url","value":"https://stg.example.com"},{"key":"api_secret","value":"s3cr3t","type":"secret","enabled":true}]}`
	envPrev, err := Preview(st, pr.ID, PreviewInput{Content: []byte(pmEnv)})
	if err != nil || envPrev.Report.EnvsFound != 1 {
		t.Fatalf("postman env preview = %+v err=%v", envPrev, err)
	}
	envRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(pmEnv)})
	if err != nil || envRes.Report.EnvsCreated != 1 || envRes.Report.EnvsFound != 1 {
		t.Fatalf("postman env apply = %+v err=%v", envRes, err)
	}

	// HAR 1.2 filters static assets and :pseudo headers
	har := `{
	  "log": {
	    "version": "1.2",
	    "entries": [
	      {"request": {"method": "GET", "url": "https://cdn.example.com/app.js", "headers": []}},
	      {"request": {"method": "GET", "url": "data:text/plain,hi", "headers": []}},
	      {
	        "request": {
	          "method": "POST",
	          "url": "https://api.example.com/v1/login?ref=web",
	          "headers": [
	            {"name": ":method", "value": "POST"},
	            {"name": "Authorization", "value": "Bearer tok-har"},
	            {"name": "Content-Type", "value": "application/json"}
	          ],
	          "postData": {"mimeType": "application/json", "text": "{\"u\":\"bob\"}"}
	        }
	      }
	    ]
	  }
	}`
	harRes, err := Apply(st, pr.ID, PreviewInput{Content: []byte(har)})
	if err != nil || harRes.Report.Format != FormatHAR || harRes.Report.APIsCreated != 1 || len(harRes.Report.Skipped) < 2 {
		t.Fatalf("har apply = %+v err=%v", harRes, err)
	}

	// cURL import into target folder
	fld, err := st.CreateFolder(pr.ID, &store.Folder{Name: "ImportedCurl"})
	if err != nil {
		t.Fatal(err)
	}
	curlRes, err := Apply(st, pr.ID, PreviewInput{
		Content:        []byte(`curl -X DELETE https://api.example.com/sessions/1`),
		TargetFolderID: fld.ID,
	})
	if err != nil || curlRes.Report.APIsCreated != 1 {
		t.Fatalf("curl apply = %+v err=%v", curlRes, err)
	}
}

func TestArchiveZipSecurity(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	pr, _ := st.CreateProject(&store.Project{Name: "zipsec"})

	buildZip := func(name string, mode os.FileMode) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fh := &zip.FileHeader{Name: name, Method: zip.Deflate}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("evil"))
		_ = zw.Close()
		return buf.Bytes()
	}

	for _, bad := range []struct {
		name string
		mode os.FileMode
	}{
		{"../escape.yaml", 0o644},
		{`apis\win.yaml`, 0o644},
		{"/abs/file.yaml", 0o644},
		{"apis/sym.yaml", os.ModeSymlink | 0o777},
	} {
		if _, err := Preview(st, pr.ID, PreviewInput{Format: FormatTidegate, Content: buildZip(bad.name, bad.mode)}); err == nil || !strings.Contains(err.Error(), "路径") && !strings.Contains(err.Error(), "符号链接") {
			t.Fatalf("expected security rejection for %q (%v), got %v", bad.name, bad.mode, err)
		}
	}
}
