package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewSPAHandlerServesAssetsAndFallback(t *testing.T) {
	if h := newSPAHandler(fstest.MapFS{}); h != nil {
		t.Fatal("expected nil handler when index.html is missing")
	}

	fsys := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><html><body>Tidegate UI</body></html>")},
		"favicon.svg":       {Data: []byte("<svg></svg>")},
		"assets/app-123.js": {Data: []byte("console.log('ok');")},
	}
	h := newSPAHandler(fsys)
	if h == nil {
		t.Fatal("expected non-nil SPA handler")
	}

	// Root & arbitrary client-side routes serve index.html with no-cache.
	for _, p := range []string{"/", "/projects/abc/workbench"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("%s Content-Type = %q", p, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s Cache-Control = %q", p, cc)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		if !strings.Contains(string(body), "Tidegate UI") {
			t.Fatalf("%s missing index html body", p)
		}
	}

	// Hashed asset receives immutable cache header.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-123.js", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset response code=%d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// Missing .js asset returns 404 rather than HTML.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", rec.Code)
	}
}
