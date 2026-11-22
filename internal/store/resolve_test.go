package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"tidegate/internal/engine"
	"tidegate/internal/scenario"
	"tidegate/internal/testserver"
)

func TestResolveInheritancePathParamsAndCompileRun(t *testing.T) {
	srv, err := testserver.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	defer srv.Close()

	s := openTestStore(t)
	pr, err := s.CreateProject(&Project{
		Name: "全链路解析",
		Variables: KeyValueList{
			{Name: "proj_var", Value: "p1"},
			{Name: "overridden", Value: "from_proj"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.CreateEnvironment(pr.ID, &Environment{
		Name:    "本地",
		BaseURL: srv.URL(),
		Variables: KeyValueList{
			{Name: "overridden", Value: "from_env"},
			{Name: "disabled_var", Value: "nope", Disabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFile(pr.ID, "users.csv", strings.NewReader("name,pwd\nalice,s3cr3t\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFile(pr.ID, "raw.bin", strings.NewReader("hello-bin"), ""); err != nil {
		t.Fatal(err)
	}

	rootFolder, err := s.CreateFolder(pr.ID, &Folder{
		Name: "根模块",
		Headers: KeyValueList{
			{Name: "X-Common", Value: "root"},
			{Name: "X-Override", Value: "root"},
			{Name: "X-Disabled", Value: "ignore", Disabled: true},
		},
		Auth: &Auth{Type: AuthBearer, Token: "parent-tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	childFolder, err := s.CreateFolder(pr.ID, &Folder{
		Name:     "子模块",
		ParentID: rootFolder.ID,
		Headers: KeyValueList{
			{Name: "x-override", Value: "child"}, // case-insensitive override
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	loginAPI, err := s.CreateAPI(pr.ID, &API{
		Name:     "登录",
		FolderID: childFolder.ID,
		Auth:     &Auth{Type: AuthNone}, // explicitly stops parent Bearer auth
		Request: APIRequest{
			Method: "POST",
			URL:    "/login",
			Body: &APIBody{
				Type:    "json",
				Content: `{"user":"{{users.name}}","pwd":"{{users.pwd}}"}`,
			},
		},
		Extract: []scenario.Extract{
			{Name: "token", From: "jsonpath", Expr: "$.data.token"},
		},
		Assert: []scenario.Assert{
			{Type: "status", Equals: ptrScalar("200")},
			{Type: "status", Disabled: true}, // ignored
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	itemsAPI, err := s.CreateAPI(pr.ID, &API{
		Name:     "查询",
		FolderID: childFolder.ID,
		Auth:     &Auth{Type: AuthBearer, Token: "{{token}}"},
		Request: APIRequest{
			Method: "GET",
			URL:    "/items",
			Query: KeyValueList{
				{Name: "q", Value: "{{overridden}}"},
				{Name: "q", Value: "{{proj_var}}"},
				{Name: "off", Value: "1", Disabled: true},
			},
		},
		Assert: []scenario.Assert{
			{Type: "status", Equals: ptrScalar("200")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check PreviewEffectiveAPI on itemsAPI.
	pv, err := s.PreviewEffectiveAPI(pr.ID, itemsAPI, ResolveOptions{EnvironmentID: env.ID})
	if err != nil {
		t.Fatal(err)
	}
	if pv.URL != srv.URL()+"/items" || pv.BaseURL != srv.URL() {
		t.Fatalf("preview URL = %+v", pv)
	}
	if pv.EffectiveAuth.Source != "api" || pv.EffectiveAuth.Auth.Type != AuthBearer {
		t.Fatalf("effective auth = %+v", pv.EffectiveAuth)
	}
	if len(pv.InheritedHeaders) != 3 || pv.InheritedHeaders[0].Overridden || !pv.InheritedHeaders[1].Overridden || pv.InheritedHeaders[2].Overridden {
		t.Fatalf("inherited headers = %+v", pv.InheritedHeaders)
	}
	var sawOverridden string
	for _, rv := range pv.Variables {
		if rv.Name == "overridden" {
			sawOverridden = rv.Value + "@" + rv.Source
		}
	}
	if sawOverridden != "from_env@environment" {
		t.Fatalf("expected overridden=from_env@environment, got %q (vars=%+v)", sawOverridden, pv.Variables)
	}

	// Build and run a multi-step scenario.
	sc, err := s.CreateScenario(pr.ID, &Scenario{
		Name:          "登录查询",
		EnvironmentID: env.ID,
		Data:          []scenario.DataSource{{Name: "users", File: "users.csv"}},
		Steps: []ScenarioStep{
			{ID: "login", APIID: loginAPI.ID},
			{ID: "query", APIID: itemsAPI.ID},
		},
		Load: scenario.Load{
			Mode:        "vus",
			Stages:      []scenario.Stage{{Duration: scenario.Duration(2 * time.Second), Target: 1}},
			MaxRequests: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ResolveScenario(pr.ID, sc.ID, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := res.Compile()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(plan, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range eng.Snapshots() {
	}
	sum := eng.Summary()
	if sum.Total.Requests != 2 || sum.Total.ErrorCount() != 0 {
		t.Fatalf("run summary: requests=%d errors=%v samples=%+v", sum.Total.Requests, sum.Total.Errors, sum.Samples)
	}
}

func TestResolvePathParamsDynamicBasicAuthAndBinary(t *testing.T) {
	s := openTestStore(t)
	pr, _ := s.CreateProject(&Project{Name: "高级解析"})
	env, _ := s.CreateEnvironment(pr.ID, &Environment{
		Name:      "dev",
		BaseURL:   "http://127.0.0.1:8080",
		Variables: KeyValueList{{Name: "uid", Value: "42"}},
	})
	_, _ = s.SaveFile(pr.ID, "doc.bin", strings.NewReader("payload"), "")

	api, err := s.CreateAPI(pr.ID, &API{
		Name: "上传",
		Auth: &Auth{Type: AuthBasic, Username: "{{$randomString(4)}}", Password: "pw"},
		Request: APIRequest{
			Method: "POST",
			URL:    "/users/{id}/files/:kind",
			PathParams: KeyValueList{
				{Name: "id", Value: "{{uid}}"},
				{Name: "kind", Value: "avatar"},
			},
			Body: &APIBody{Type: "binary", File: "doc.bin", ContentType: "application/octet-stream"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.ResolveAPIDraft(pr.ID, api, APIRunOptions{ResolveOptions: ResolveOptions{EnvironmentID: env.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Definition.Steps[0].Request.URL; got != "http://127.0.0.1:8080/users/{{uid}}/files/avatar" {
		t.Fatalf("URL = %q", got)
	}
	if res.Definition.Steps[0].Request.BasicAuth == nil {
		t.Fatal("expected runtime BasicAuth when username contains built-in function")
	}
	plan, err := res.Compile()
	if err != nil {
		t.Fatal(err)
	}
	st := plan.Steps[0].(*scenario.HTTPStep)
	if st.BasicUser == nil || st.Body.Literal() != "payload" {
		t.Fatalf("compiled step = %+v", st)
	}

	// Static BasicAuth folds into Authorization header cleanly.
	staticAPI := &API{
		Name: "static-basic",
		Auth: &Auth{Type: AuthBasic, Username: "alice", Password: "123"},
		Request: APIRequest{
			URL: "http://127.0.0.1:8080/ping",
		},
	}
	resStatic, err := s.ResolveAPIDraft(pr.ID, staticAPI, APIRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	for _, h := range resStatic.Definition.Steps[0].Request.HeaderList {
		if h.Name == "Authorization" {
			gotAuth = string(h.Value)
		}
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:123"))
	if gotAuth != wantAuth {
		t.Fatalf("static basic auth = %q, want %q", gotAuth, wantAuth)
	}

	// Incomplete bearer auth is allowed on save, rejected on resolve.
	draftBearer, err := s.CreateAPI(pr.ID, &API{
		Name:    "incomplete-bearer",
		Auth:    &Auth{Type: AuthBearer},
		Request: APIRequest{URL: "http://127.0.0.1:8080/ping"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPI(pr.ID, draftBearer.ID, APIRunOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete bearer resolve: got %v, want ErrInvalid", err)
	}
}

func ptrScalar(s string) *scenario.Scalar {
	v := scenario.Scalar(s)
	return &v
}
