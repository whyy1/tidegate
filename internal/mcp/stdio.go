package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// StdioOptions configures the local stdio-to-Streamable-HTTP MCP proxy.
type StdioOptions struct {
	Endpoint   string
	Token      string
	CAFile     string
	Insecure   bool
	HTTPClient *http.Client
}

// NormalizeEndpoint turns a base URL (e.g. https://127.0.0.1:8080) or full
// endpoint URL into the canonical /api/v1/mcp URL.
func NormalizeEndpoint(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("必须指定 Tidegate 服务地址（--url 或 TIDEGATE_URL）")
	}
	s = strings.TrimRight(s, "/")
	if strings.HasSuffix(s, "/api/v1/mcp") {
		return s, nil
	}
	return s + "/api/v1/mcp", nil
}

// RunStdioProxy bridges newline-delimited JSON-RPC frames on stdin/stdout to
// the target Tidegate Streamable HTTP /api/v1/mcp endpoint. All diagnostics
// are emitted strictly to stderr so stdout carries valid JSON-RPC frames only.
func RunStdioProxy(ctx context.Context, opts StdioOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	endpoint, err := NormalizeEndpoint(opts.Endpoint)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		return errors.New("必须提供访问令牌（--token 或 TIDEGATE_TOKEN）")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	client := opts.HTTPClient
	if client == nil {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if opts.Insecure {
			tlsCfg.InsecureSkipVerify = true
		} else if strings.TrimSpace(opts.CAFile) != "" {
			pemData, rerr := os.ReadFile(strings.TrimSpace(opts.CAFile))
			if rerr != nil {
				return fmt.Errorf("读取 CA 证书文件失败：%w", rerr)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemData) {
				return errors.New("解析 CA PEM 证书失败")
			}
			tlsCfg.RootCAs = pool
		}
		client = &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsCfg,
			},
		}
	}

	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		respBytes, err := forwardJSONRPC(ctx, client, endpoint, token, line)
		if err != nil {
			if stderr != nil {
				_, _ = fmt.Fprintf(stderr, "tidegate mcp: %v\n", err)
			}
			errFrame := synthesizeErrorFrame(line, err)
			if len(errFrame) > 0 {
				_, _ = stdout.Write(append(errFrame, '\n'))
			}
			continue
		}
		if len(respBytes) == 0 {
			continue
		}
		respBytes = bytes.TrimSpace(respBytes)
		if len(respBytes) == 0 {
			continue
		}
		_, _ = stdout.Write(append(respBytes, '\n'))
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("读取标准输入失败：%w", err)
	}
	return nil
}

func forwardJSONRPC(ctx context.Context, client *http.Client, endpoint, token string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func synthesizeErrorFrame(rawReq []byte, cause error) []byte {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(rawReq, &msg); err != nil || len(msg.ID) == 0 || string(msg.ID) == "null" {
		return nil
	}
	out, err := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Error: &rpcError{
			Code:    CodeServerError,
			Message: cause.Error(),
		},
	})
	if err != nil {
		return nil
	}
	return out
}
