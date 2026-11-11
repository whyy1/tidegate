package testserver

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T) *Server {
	t.Helper()
	s, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	t.Cleanup(func() { s.Close() })
	return s
}

func get(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestRoutes(t *testing.T) {
	s := start(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := s.URL()

	if resp, body := get(t, c, base+"/ping"); resp.StatusCode != 200 || body != "pong" {
		t.Fatalf("ping: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(t, c, base+"/status?code=503"); resp.StatusCode != 503 || body != "status 503\n" {
		t.Fatalf("status: %d %q", resp.StatusCode, body)
	}
	if resp, _ := get(t, c, base+"/status?code=204"); resp.StatusCode != 204 {
		t.Fatalf("status 204: %d", resp.StatusCode)
	}
	if resp, body := get(t, c, base+"/bytes?n=200000"); resp.StatusCode != 200 || len(body) != 200000 || resp.ContentLength != 200000 {
		t.Fatalf("bytes: %d len=%d", resp.StatusCode, len(body))
	}
	start := time.Now()
	if resp, _ := get(t, c, base+"/delay?ms=50"); resp.StatusCode != 200 || time.Since(start) < 45*time.Millisecond {
		t.Fatalf("delay: %d after %v", resp.StatusCode, time.Since(start))
	}
	if resp, _ := get(t, c, base+"/redirect?to=/status?code=201"); resp.StatusCode != 302 || resp.Header.Get("Location") != "/status?code=201" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := get(t, c, base+"/nope"); resp.StatusCode != 404 {
		t.Fatalf("404: %d", resp.StatusCode)
	}

	resp, err := c.Post(base+"/echo", "text/x-test", strings.NewReader("hello echo"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hello echo" || resp.Header.Get("Content-Type") != "text/x-test" {
		t.Fatalf("echo: %q %q", b, resp.Header.Get("Content-Type"))
	}

	// Chunked request body (unknown length).
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte("chunk-1;")); pw.Write([]byte("chunk-2")); pw.Close() }()
	resp, err = c.Post(base+"/echo", "text/plain", pr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "chunk-1;chunk-2" {
		t.Fatalf("chunked echo: %q", b)
	}
}

func TestLoginAndItems(t *testing.T) {
	s := start(t)
	base := s.URL()
	resp, err := http.Post(base+"/login", "application/json", strings.NewReader(`{"user":"alice","pwd":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		Code int
		Data struct{ User, Token string }
	}
	json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	if resp.StatusCode != 200 || login.Code != 0 || login.Data.Token == "" || login.Data.User != "alice" {
		t.Fatalf("login: %d %+v", resp.StatusCode, login)
	}

	items := func(auth string) (int, string) {
		req, _ := http.NewRequest("GET", base+"/items", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := items("Bearer " + login.Data.Token); code != 200 || !strings.Contains(body, `"user":"alice"`) || !strings.Contains(body, "banana") {
		t.Fatalf("items: %d %s", code, body)
	}
	for _, bad := range []string{"", "Bearer nope", "Bearer " + login.Data.Token + "x", "Basic abc"} {
		if code, _ := items(bad); code != 401 {
			t.Fatalf("items with %q: %d", bad, code)
		}
	}
	for body, want := range map[string]int{`{"user":"a","pwd":"wrong"}`: 401, `{"user":"a"}`: 401, `not json`: 400} {
		resp, _ := http.Post(base+"/login", "application/json", strings.NewReader(body))
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("login %s: %d, want %d", body, resp.StatusCode, want)
		}
	}
	if resp, _ := http.Get(base + "/login"); resp.StatusCode != 405 {
		t.Fatalf("GET /login: %d", resp.StatusCode)
	}
}

func TestPipeliningAndClose(t *testing.T) {
	s := start(t)
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	// Three pipelined requests; the last one asks to close.
	_, err = c.Write([]byte("GET /ping HTTP/1.1\r\nHost: x\r\n\r\n" +
		"POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc" +
		"GET /ping HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	for i, want := range []string{"pong", "abc", "pong"} {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		b, _ := io.ReadAll(resp.Body)
		if string(b) != want {
			t.Fatalf("response %d = %q", i, b)
		}
		if i == 2 && !resp.Close {
			t.Fatal("last response should close the connection")
		}
	}
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("connection not closed: %v", err)
	}
}

func TestHTTP10AndBadRequest(t *testing.T) {
	s := start(t)
	c, _ := net.Dial("tcp", s.Addr())
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET /ping HTTP/1.0\r\n\r\n"))
	b, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(b), "HTTP/1.1 200") || !strings.HasSuffix(string(b), "pong") {
		t.Fatalf("http/1.0: %q", b)
	}

	c2, _ := net.Dial("tcp", s.Addr())
	defer c2.Close()
	c2.SetDeadline(time.Now().Add(5 * time.Second))
	c2.Write([]byte("garbage\r\n\r\n"))
	b, _ = io.ReadAll(c2)
	if !strings.HasPrefix(string(b), "HTTP/1.1 400") {
		t.Fatalf("bad request: %q", b)
	}
}

func TestExpectContinue(t *testing.T) {
	s := start(t)
	c, _ := net.Dial("tcp", s.Addr())
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\nExpect: 100-continue\r\n\r\n"))
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	if !strings.HasPrefix(line, "HTTP/1.1 100") {
		t.Fatalf("expected 100 Continue, got %q", line)
	}
	br.ReadString('\n')
	c.Write([]byte("hi"))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hi" {
		t.Fatalf("body %q", b)
	}
}

func TestCloseStopsServing(t *testing.T) {
	s, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	c, _ := net.Dial("tcp", s.Addr())
	c.Write([]byte("GET /ping HTTP/1.1\r\nHost: x\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with an open keep-alive connection")
	}
	if s.Requests() != 1 {
		t.Fatalf("requests = %d", s.Requests())
	}
}
