package exporter

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tidegate/internal/importer"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

func TestExportOpenAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(&store.Project{
		Name:        "Orders API",
		Description: "订单服务接口",
		Variables: store.KeyValueList{
			{Name: "proj_secret", Value: "super-secret-proj", Secret: true},
			{Name: "proj_env", Value: "staging"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEnvironment(pr.ID, &store.Environment{
		Name:    "Prod",
		BaseURL: "https://{{host}}/v1",
		Variables: store.KeyValueList{
			{Name: "host", Value: "api.example.com"},
			{Name: "env_secret", Value: "env-secret-val", Secret: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	folder, err := st.CreateFolder(pr.ID, &store.Folder{
		Name: "Orders",
		Auth: &store.Auth{Type: store.AuthBearer, Token: "{{bearer_token}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name:     "GetOrder",
		FolderID: folder.ID,
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/orders/:id",
			PathParams: store.KeyValueList{
				{Name: "id", Value: "ord-100", Description: "订单号"},
			},
			Query: store.KeyValueList{
				{Name: "expand", Value: "items"},
				{Name: "token", Value: "raw-query-secret", Secret: true},
			},
			Headers: store.KeyValueList{
				{Name: "X-Trace", Value: "trace-1"},
				{Name: "X-Internal-Key", Value: "hdr-secret-val", Secret: true},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name: "CreateOrder",
		Auth: &store.Auth{Type: store.AuthBasic, Username: "svc", Password: "raw-basic-password"},
		Request: store.APIRequest{
			Method: "POST",
			URL:    "/orders",
			Body: &store.APIBody{
				Type:    "json",
				Content: `{"item":"book","qty":1}`,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	jsonBytes, err := ExportOpenAPI(st, pr.ID, false)
	if err != nil {
		t.Fatalf("ExportOpenAPI(JSON): %v", err)
	}
	yamlBytes, err := ExportOpenAPI(st, pr.ID, true)
	if err != nil {
		t.Fatalf("ExportOpenAPI(YAML): %v", err)
	}

	for _, secret := range []string{"super-secret-proj", "env-secret-val", "raw-query-secret", "hdr-secret-val", "raw-basic-password"} {
		if strings.Contains(string(jsonBytes), secret) || strings.Contains(string(yamlBytes), secret) {
			t.Fatalf("exported OpenAPI leaked secret %q", secret)
		}
	}

	var doc map[string]any
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		t.Fatalf("invalid OpenAPI JSON: %v", err)
	}
	if got, _ := doc["openapi"].(string); got != "3.0.3" {
		t.Fatalf("openapi version = %q, want 3.0.3", got)
	}
	paths, _ := doc["paths"].(map[string]any)
	if _, ok := paths["/orders/{id}"]; !ok {
		t.Fatalf("expected /orders/{id} in paths, got %#v", paths)
	}
	comps, _ := doc["components"].(map[string]any)
	secSchemes, _ := comps["securitySchemes"].(map[string]any)
	if len(secSchemes) < 2 {
		t.Fatalf("expected bearer & basic securitySchemes, got %#v", secSchemes)
	}

	// Re-import exported OpenAPI YAML via importer.Preview cleanly.
	dstPr, err := st.CreateProject(&store.Project{Name: "Reimport Check"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := importer.Preview(st, dstPr.ID, importer.PreviewInput{
		Filename: "orders.yaml",
		Content:  yamlBytes,
	})
	if err != nil {
		t.Fatalf("importer.Preview(exported YAML): %v", err)
	}
	if prev.Report.Format != importer.FormatOpenAPI3 || prev.Report.APIsFound != 2 {
		t.Fatalf("unexpected re-import preview: %+v", prev.Report)
	}
}

func TestExportArchiveRoundTrip(t *testing.T) {
	src, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := src.CreateProject(&store.Project{
		Name: "Checkout",
		Variables: store.KeyValueList{
			{Name: "public_flag", Value: "on"},
			{Name: "master_secret", Value: "do-not-leak-proj", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := src.CreateEnvironment(pr.ID, &store.Environment{
		Name:    "Staging",
		BaseURL: "http://127.0.0.1:9000",
		Variables: store.KeyValueList{
			{Name: "api_key", Value: "do-not-leak-env", Secret: true},
			{Name: "region", Value: "hz"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fld, err := src.CreateFolder(pr.ID, &store.Folder{Name: "Cart"})
	if err != nil {
		t.Fatal(err)
	}
	apiItem, err := src.CreateAPI(pr.ID, &store.API{
		Name:     "AddItem",
		FolderID: fld.ID,
		Auth:     &store.Auth{Type: store.AuthBearer, Token: "literal-secret-token"},
		Request: store.APIRequest{
			Method: "POST",
			URL:    "/cart/add",
			Headers: store.KeyValueList{
				{Name: "X-Secret-Hdr", Value: "literal-hdr-secret", Secret: true},
				{Name: "X-Templ-Hdr", Value: "{{token}}"},
			},
			Body: &store.APIBody{Type: "json", Content: `{"sku":"{{sku}}"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	apiTemplAuth, err := src.CreateAPI(pr.ID, &store.API{
		Name: "GetCart",
		Auth: &store.Auth{Type: store.AuthBearer, Token: "{{api_token}}"},
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/cart",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	csvBody := "sku\nSKU-001\nSKU-002\n"
	if _, err := src.SaveFile(pr.ID, "skus.csv", strings.NewReader(csvBody), ""); err != nil {
		t.Fatal(err)
	}

	if _, err := src.CreateScenario(pr.ID, &store.Scenario{
		Name:          "Cart Flow",
		EnvironmentID: env.ID,
		Data:          []scenario.DataSource{{Name: "skus", File: "skus.csv"}},
		Steps: []store.ScenarioStep{
			{ID: "step_add", APIID: apiItem.ID, Disabled: true},
			{ID: "step_get", APIID: apiTemplAuth.ID},
		},
		Load: scenario.Load{
			Stages: []scenario.Stage{{Duration: scenario.Duration(time.Second), Target: 1}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := ExportArchive(src, pr.ID, &buf); err != nil {
		t.Fatalf("ExportArchive: %v", err)
	}
	zipBytes := buf.Bytes()
	for _, secret := range []string{"do-not-leak-proj", "do-not-leak-env", "literal-secret-token", "literal-hdr-secret"} {
		if bytes.Contains(zipBytes, []byte(secret)) {
			t.Fatalf("archive bytes leaked literal secret %q", secret)
		}
	}

	dst, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	importedPr, report, err := importer.ImportProjectArchive(dst, zipBytes, "Restored Checkout")
	if err != nil {
		t.Fatalf("ImportProjectArchive: %v", err)
	}
	if importedPr.Name != "Restored Checkout" {
		t.Fatalf("imported project name = %q", importedPr.Name)
	}
	if report.ScenariosCreated != 1 || report.FilesImported != 1 || report.EnvsCreated != 1 || report.APIsCreated != 2 {
		t.Fatalf("unexpected archive report: %+v", report)
	}

	// Verify secret variable values were scrubbed to empty and template preserved.
	var foundTemplAuth bool
	tree, err := dst.Tree(importedPr.ID)
	if err != nil {
		t.Fatal(err)
	}
	var walk func(nodes []*store.TreeNode)
	walk = func(nodes []*store.TreeNode) {
		for _, n := range nodes {
			if n.Type == store.NodeFolder {
				walk(n.Children)
				continue
			}
			a, err := dst.GetAPI(importedPr.ID, n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if a.Name == "AddItem" && a.Auth != nil && a.Auth.Token != "" {
				t.Fatalf("expected literal bearer token scrubbed, got %q", a.Auth.Token)
			}
			if a.Name == "AddItem" {
				var foundSecretCleared bool
				for _, h := range a.Request.Headers {
					if h.Name == "X-Secret-Hdr" && h.Secret && string(h.Value) == "" {
						foundSecretCleared = true
					}
					if h.Name == "X-Templ-Hdr" && string(h.Value) == "{{token}}" {
						foundTemplAuth = true
					}
				}
				if !foundSecretCleared {
					t.Fatal("expected Secret header value to be scrubbed while keeping row structure")
				}
			}
			if a.Name == "GetCart" && (a.Auth == nil || a.Auth.Token != "{{api_token}}") {
				t.Fatalf("expected template bearer token preserved, got %+v", a.Auth)
			}
		}
	}
	walk(tree.Children)
	if !foundTemplAuth {
		t.Fatal("expected secret template expression {{token}} to be preserved in archive export")
	}

	scens, err := dst.ListScenarios(importedPr.ID)
	if err != nil || len(scens) != 1 {
		t.Fatalf("ListScenarios = (%v, %v)", scens, err)
	}
	if _, err := dst.ResolveScenario(importedPr.ID, scens[0].ID, store.ResolveOptions{}); err != nil {
		t.Fatalf("ResolveScenario after archive round-trip failed: %v", err)
	}
	csvPath, err := dst.FilePath(importedPr.ID, "skus.csv")
	if err != nil {
		t.Fatalf("FilePath(skus.csv): %v", err)
	}
	gotCSV, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("ReadFile(skus.csv): %v", err)
	}
	if string(gotCSV) != csvBody {
		t.Fatalf("restored CSV = %q, want %q", string(gotCSV), csvBody)
	}
}

func TestExportRunHTML(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	summary := metrics.SnapshotView{
		Seq:         2,
		Time:        now.Add(2 * time.Second),
		DurationMs:  2000,
		ActiveVUs:   10,
		ActiveConns: 10,
		StepView: metrics.StepView{
			Requests:  200,
			RPS:       100,
			Errors:    2,
			ErrorRate: 0.01,
			Latency: metrics.LatencyStats{
				P50: 12.5,
				P95: 45.2,
				P99: 88.1,
				Max: 120.4,
			},
			BytesSent:     10240,
			BytesReceived: 40960,
		},
		Steps: []metrics.StepView{
			{
				ID:        "s1",
				Name:      "Login <script>bad()</script>",
				Requests:  200,
				RPS:       100,
				Errors:    2,
				ErrorRate: 0.01,
				Latency:   metrics.LatencyStats{P50: 12.5, P95: 45.2, P99: 88.1, Max: 120.4},
			},
		},
	}
	sumJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	snap1, _ := json.Marshal(metrics.SnapshotView{
		Seq:  1,
		Time: now.Add(time.Second),
		StepView: metrics.StepView{
			Requests: 100,
			RPS:      100,
			Latency:  metrics.LatencyStats{P50: 10, P95: 30, P99: 50},
		},
	})

	run := &store.Run{
		ID:           "run-01",
		ProjectID:    "proj-01",
		ScenarioName: `Smoke <script>alert(1)</script>`,
		Status:       store.RunFinished,
		CreatedAt:    now,
		StartedAt:    now,
		FinishedAt:   now.Add(2 * time.Second),
		Summary:      sumJSON,
	}
	snaps := []store.SnapshotRecord{
		{Seq: 1, Time: now.Add(time.Second), Data: snap1},
		{Seq: 2, Time: now.Add(2 * time.Second), Data: sumJSON},
	}
	alerts := []store.Alert{
		{
			ID:        1,
			RunID:     run.ID,
			Agent:     "local",
			Kind:      "cpu",
			Level:     store.AlertWarning,
			Message:   "CPU 利用率超过 85%",
			Value:     88.5,
			Threshold: 85,
			StartedAt: now.Add(time.Second),
		},
	}
	sampleJSON, _ := json.Marshal(metrics.SampleView{
		Kind:            "error",
		Step:            "s1",
		Time:            now.Add(time.Second),
		Method:          "POST",
		URL:             "http://127.0.0.1/login",
		Status:          500,
		RequestHeaders:  []metrics.Header{{Name: "Authorization", Value: "Bearer secret-tok"}, {Name: "X-Custom", Value: "ok"}},
		ResponseHeaders: []metrics.Header{{Name: "Set-Cookie", Value: "sid=secret"}},
		Body:            `<script>alert("xss")</script>`,
	})
	samples := []store.SampleRecord{
		{ID: 1, Seq: 1, Kind: "error", Step: "s1", Time: now.Add(time.Second), Data: sampleJSON},
	}

	var out bytes.Buffer
	if err := ExportRunHTML(run, snaps, alerts, samples, &out); err != nil {
		t.Fatalf("ExportRunHTML: %v", err)
	}
	htmlStr := out.String()

	for _, want := range []string{
		"<!DOCTYPE html>",
		"Tidegate 压测报告",
		`id="c-rps"`,
		`id="c-lat"`,
		`id="c-bw"`,
		`id="c-vus"`,
		"CPU 利用率超过 85%",
		"按步骤统计",
		`id="tidegate-report-data"`,
	} {
		if !strings.Contains(htmlStr, want) {
			t.Fatalf("HTML report missing %q", want)
		}
	}

	for _, forbidden := range []string{
		`<script>alert(1)</script>`,
		`<script>bad()</script>`,
		`<script>alert("xss")</script>`,
		`Bearer secret-tok`,
		`sid=secret`,
	} {
		if strings.Contains(htmlStr, forbidden) {
			t.Fatalf("HTML report contained unescaped or unredacted string %q", forbidden)
		}
	}
	if !strings.Contains(htmlStr, `[REDACTED]`) {
		t.Fatal("expected sensitive headers in HTML report samples to be replaced with [REDACTED]")
	}
	if !strings.Contains(htmlStr, `&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Fatal("expected scenario name to be HTML-escaped in title/header")
	}
	if !strings.Contains(htmlStr, `\u003cscript\u003ealert(\"xss\")\u003c/script\u003e`) {
		t.Fatal("expected JSON payload inside script tag to escape < and > as \\u003c / \\u003e")
	}
}

func TestExportPostmanAndHARRoundTrip(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(&store.Project{
		Name: "Payments",
		Variables: store.KeyValueList{
			{Name: "api_secret", Value: "pm-secret", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEnvironment(pr.ID, &store.Environment{
		Name:    "Prod",
		BaseURL: "https://pay.example.com",
	}); err != nil {
		t.Fatal(err)
	}
	fld, err := st.CreateFolder(pr.ID, &store.Folder{Name: "Charges"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name:     "GetCharge",
		FolderID: fld.ID,
		Auth:     &store.Auth{Type: store.AuthBearer, Token: "{{token}}"},
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/charges/:cid",
			PathParams: store.KeyValueList{
				{Name: "cid", Value: "ch_1"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name: "CreateCharge",
		Request: store.APIRequest{
			Method: "POST",
			URL:    "/charges",
			Body:   &store.APIBody{Type: "json", Content: `{"amount":100}`},
		},
	}); err != nil {
		t.Fatal(err)
	}

	pmBytes, err := ExportPostman(st, pr.ID)
	if err != nil {
		t.Fatalf("ExportPostman: %v", err)
	}
	if bytes.Contains(pmBytes, []byte("pm-secret")) {
		t.Fatal("ExportPostman leaked secret variable")
	}
	dstPr, _ := st.CreateProject(&store.Project{Name: "PM Import"})
	pmPrev, err := importer.Preview(st, dstPr.ID, importer.PreviewInput{
		Filename: "payments-postman.json",
		Content:  pmBytes,
	})
	if err != nil {
		t.Fatalf("importer.Preview(ExportPostman): %v", err)
	}
	if pmPrev.Report.Format != importer.FormatPostman || pmPrev.Report.APIsFound != 2 {
		t.Fatalf("unexpected Postman preview: %+v", pmPrev.Report)
	}

	// Verify HAR 1.2 export of samples with redaction and round-trip into importer.
	now := time.Now().UTC()
	smpJSON, _ := json.Marshal(metrics.SampleView{
		Kind:   "error",
		Step:   "charge",
		Time:   now,
		Method: "POST",
		URL:    "https://pay.example.com/v1/charges?mode=test",
		Status: 502,
		RequestHeaders: []metrics.Header{
			{Name: "Authorization", Value: "Bearer har-secret"},
			{Name: "Content-Type", Value: "application/json"},
		},
		RequestBody: `{"amount":100}`,
		Body:        `{"error":"bad gateway"}`,
	})
	harBytes, err := ExportSamplesHAR(&store.Run{ID: "r1", ScenarioName: "Pay"}, []store.SampleRecord{
		{ID: 1, Seq: 1, Kind: "error", Step: "charge", Time: now, Data: smpJSON},
	})
	if err != nil {
		t.Fatalf("ExportSamplesHAR: %v", err)
	}
	if bytes.Contains(harBytes, []byte("har-secret")) || !bytes.Contains(harBytes, []byte("[REDACTED]")) {
		t.Fatal("ExportSamplesHAR failed sensitive header redaction")
	}
	harPrev, err := importer.Preview(st, dstPr.ID, importer.PreviewInput{
		Filename: "samples.har",
		Content:  harBytes,
	})
	if err != nil {
		t.Fatalf("importer.Preview(ExportSamplesHAR): %v", err)
	}
	if harPrev.Report.Format != importer.FormatHAR || harPrev.Report.APIsFound != 1 {
		t.Fatalf("unexpected HAR preview: %+v", harPrev.Report)
	}
}

func TestExportOpenAPIOAuth2ExamplesAndResponseFields(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(&store.Project{Name: "OAuth2 Docs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPI(pr.ID, &store.API{
		Name: "GetProfile",
		Auth: &store.Auth{
			Type:         store.AuthOAuth2,
			TokenURL:     "https://auth.example.com/oauth/token",
			ClientID:     "client-pub",
			ClientSecret: "super-oauth-client-secret",
			Scope:        "profile email",
		},
		Request: store.APIRequest{
			Method: "GET",
			URL:    "https://api.example.com/v1/profile",
		},
		Examples: []store.APIResponseExample{
			{
				Name:        "ok_json",
				Status:      200,
				ContentType: "application/json",
				Headers: store.KeyValueList{
					{Name: "X-RateLimit", Value: "100", Description: "剩余配额"},
					{Name: "Set-Cookie", Value: "sid=secret-cookie"},
				},
				Body: `{"user":{"id":"u1","age":30}}`,
			},
			{
				Name:        "cached_json",
				Status:      200,
				ContentType: "application/json",
				Body:        `{"user":{"id":"u2","age":25}}`,
			},
			{
				Name:   "not_found",
				Status: 404,
				Body:   `{"error":"missing"}`,
			},
		},
		ResponseFields: []store.APIFieldDoc{
			{Path: "$.user.id", Type: "string", Required: true, Description: "用户 ID"},
			{Path: "$.user.age", Type: "integer", Description: "年龄"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := ExportOpenAPI(st, pr.ID, false)
	if err != nil {
		t.Fatalf("ExportOpenAPI: %v", err)
	}
	if strings.Contains(string(raw), "super-oauth-client-secret") || strings.Contains(string(raw), "secret-cookie") {
		t.Fatalf("exported OpenAPI leaked OAuth2 client secret or sensitive response header: %s", raw)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	comps, _ := doc["components"].(map[string]any)
	schemes, _ := comps["securitySchemes"].(map[string]any)
	oauthScheme, _ := schemes["oauth2ClientCredentials"].(map[string]any)
	if oauthScheme == nil || oauthScheme["type"] != "oauth2" {
		t.Fatalf("missing oauth2ClientCredentials scheme: %+v", schemes)
	}
	flows, _ := oauthScheme["flows"].(map[string]any)
	cc, _ := flows["clientCredentials"].(map[string]any)
	if cc["tokenUrl"] != "https://auth.example.com/oauth/token" {
		t.Fatalf("unexpected clientCredentials flow: %+v", cc)
	}

	paths, _ := doc["paths"].(map[string]any)
	pathItem, _ := paths["/v1/profile"].(map[string]any)
	op, _ := pathItem["get"].(map[string]any)
	resps, _ := op["responses"].(map[string]any)
	r200, _ := resps["200"].(map[string]any)
	if r200 == nil || resps["404"] == nil {
		t.Fatalf("expected 200 and 404 responses, got %+v", resps)
	}
	hdrs, _ := r200["headers"].(map[string]any)
	if hdrs["X-RateLimit"] == nil || hdrs["Set-Cookie"] != nil {
		t.Fatalf("unexpected response headers export: %+v", hdrs)
	}
	content, _ := r200["content"].(map[string]any)
	appJSON, _ := content["application/json"].(map[string]any)
	examples, _ := appJSON["examples"].(map[string]any)
	if examples["ok_json"] == nil || examples["cached_json"] == nil {
		t.Fatalf("expected named response examples, got %+v", appJSON)
	}
	schema, _ := appJSON["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	userObj, _ := props["user"].(map[string]any)
	userProps, _ := userObj["properties"].(map[string]any)
	reqList, _ := userObj["required"].([]any)
	if userProps["id"] == nil || userProps["age"] == nil || len(reqList) != 1 || reqList[0] != "id" {
		t.Fatalf("unexpected ResponseFields schema: %+v", schema)
	}
}

func TestExportJTLAndK6(t *testing.T) {
	// 1. ExportRunJTL 17-column output, non-group filter, and query secret redaction.
	now := time.Unix(1700000100, 0).UTC()
	sv := metrics.SnapshotView{
		Seq:        0,
		Time:       now,
		DurationMs: 1000,
		ActiveVUs:  4,
		Steps: []metrics.StepView{
			{ID: "tx", Name: "下单事务", IsGroup: true, Requests: 10, Errors: 0, Latency: metrics.LatencyStats{Mean: 42}},
			{ID: "pay", Name: "支付请求", IsGroup: false, Requests: 10, Errors: 1, BytesSent: 1200, BytesReceived: 3400, Latency: metrics.LatencyStats{Mean: 25}, Phases: map[string]metrics.PhaseView{"first_byte": {Count: 10, AvgMs: 18}, "connect": {Count: 2, AvgMs: 5}}},
		},
	}
	snapJSON, _ := json.Marshal(&sv)
	smpJSON, _ := json.Marshal(metrics.SampleView{
		Kind:        "error",
		Step:        "支付请求",
		Time:        now.Add(100 * time.Millisecond),
		Method:      "POST",
		URL:         "https://pay.example.com/charge?token=secret-tok&order=123",
		Status:      500,
		Error:       "status 500",
		DurationMs:  35,
		RequestBody: `{"amount":1}`,
		Body:        `{"err":"fail"}`,
	})
	var jtlBuf bytes.Buffer
	err := ExportRunJTL(&store.Run{ID: "run-jtl", ScenarioName: "PayFlow"}, []store.SnapshotRecord{
		{Seq: 0, Time: now, Data: snapJSON},
	}, []store.SampleRecord{
		{ID: 1, Seq: 0, Kind: "error", Step: "支付请求", Time: now.Add(100 * time.Millisecond), Data: smpJSON},
	}, &jtlBuf)
	if err != nil {
		t.Fatalf("ExportRunJTL: %v", err)
	}
	if strings.Contains(jtlBuf.String(), "secret-tok") {
		t.Fatalf("ExportRunJTL leaked sensitive query token: %s", jtlBuf.String())
	}
	rows, err := csv.NewReader(bytes.NewReader(jtlBuf.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("parse JTL CSV: %v", err)
	}
	if len(rows) != 3 || len(rows[0]) != 17 || rows[0][0] != "timeStamp" || rows[0][16] != "Connect" {
		t.Fatalf("unexpected JTL rows (%d): %+v", len(rows), rows)
	}
	for _, r := range rows[1:] {
		if r[2] == "下单事务" {
			t.Fatalf("IsGroup step should not appear in JTL rows: %+v", r)
		}
	}

	// 2. ExportScenarioK6 with SecretVault + control flow + thresholds + data source.
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDB(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rdb := store.NewResultDB(db)
	box, err := store.LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretVault(store.NewSecretVault(rdb, box))

	pr, err := st.CreateProject(&store.Project{
		Name: "k6-export",
		Variables: store.KeyValueList{
			{Name: "api_secret", Value: "plain-k6-never-embed", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := st.CreateEnvironment(pr.ID, &store.Environment{
		Name:    "Staging",
		BaseURL: "https://api.example.com",
		Variables: store.KeyValueList{
			{Name: "env_token", Value: "env-k6-never-embed", Secret: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFile(pr.ID, "users.csv", strings.NewReader("uid\nu1\n"), ""); err != nil {
		t.Fatal(err)
	}
	apiObj, err := st.CreateAPI(pr.ID, &store.API{
		Name: "GetUser",
		Request: store.APIRequest{
			Method: "GET",
			URL:    "/users/{{users.uid}}",
			Headers: store.KeyValueList{
				{Name: "X-API-Secret", Value: "{{api_secret}}"},
				{Name: "Authorization", Value: "Bearer {{env_token}}"},
			},
		},
		Extract: []scenario.Extract{{Name: "flag", From: "jsonpath", Expr: "$.ok"}},
		Assert:  []scenario.Assert{{Type: "status", Equals: ptrScalar("200")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scen, err := st.CreateScenario(pr.ID, &store.Scenario{
		Name:          "flow",
		EnvironmentID: env.ID,
		Data:          []scenario.DataSource{{Name: "users", File: "users.csv", Mode: "sequential"}},
		Steps: []store.ScenarioStep{
			{
				ID:   "tx",
				Type: "group",
				Name: "UserFlow",
				Children: []store.ScenarioStep{
					{ID: "lp", Type: "loop", LoopCount: 2, Children: []store.ScenarioStep{{ID: "s1", APIID: apiObj.ID}}},
					{ID: "cond", Type: "if", Expr: `{{flag}} == "true"`, Children: []store.ScenarioStep{{ID: "s2", APIID: apiObj.ID}}},
				},
			},
		},
		Load: scenario.Load{
			Mode:       "vus",
			Stages:     []scenario.Stage{{Duration: scenario.Duration(10 * time.Second), Target: 5}},
			Thresholds: []scenario.Threshold{{Metric: "p95_ms", Value: 250}, {Metric: "error_rate", Value: 0.05}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	scriptBytes, err := ExportScenarioK6(st, pr.ID, scen.ID, store.ResolveOptions{})
	if err != nil {
		t.Fatalf("ExportScenarioK6: %v", err)
	}
	script := string(scriptBytes)
	if strings.Contains(script, "plain-k6-never-embed") || strings.Contains(script, "env-k6-never-embed") {
		t.Fatalf("exported k6 script leaked secret plaintext:\n%s", script)
	}
	for _, want := range []string{
		"import http from 'k6/http';",
		"thresholds:",
		"group('UserFlow'",
		"for (let i = 0; i < 2; i++)",
		"check(",
		"${__ENV.API_SECRET}",
		"${__ENV.ENV_TOKEN}",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("exported k6 script missing %q:\n%s", want, script)
		}
	}
}

func ptrScalar(s string) *scenario.Scalar {
	v := scenario.Scalar(s)
	return &v
}
