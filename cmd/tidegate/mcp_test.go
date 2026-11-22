package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMCPSubcommandUsageAndValidation(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMainWithIO([]string{"mcp", "--help"}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("mcp --help exit=%d, want 0", code)
	}
	if !strings.Contains(errBuf.String()+out.String(), "tidegate mcp") {
		t.Fatalf("expected usage text mentioning tidegate mcp")
	}

	out.Reset()
	errBuf.Reset()
	code = runMainWithIO([]string{"mcp", "--url", "https://127.0.0.1:8080"}, strings.NewReader(""), &out, &errBuf)
	if code == 0 {
		t.Fatal("expected non-zero exit when --token is omitted")
	}
	if !strings.Contains(errBuf.String(), "令牌") {
		t.Fatalf("expected missing token message in stderr, got: %s", errBuf.String())
	}
}
