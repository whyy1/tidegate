// Package mcp implements a pure-Go Model Context Protocol (MCP) server
// supporting Streamable HTTP (POST /api/v1/mcp) and local CLI stdio bridging
// with role-based access control, Vault secret redaction, SSRF allowlist
// enforcement, load-scale guardrails, and full audit logging.
package mcp

import "encoding/json"

// Protocol versions advertised during MCP initialization.
const (
	LatestProtocolVersion = "2025-06-18"
)

// SupportedProtocolVersions lists MCP protocol revisions handled by this server.
var SupportedProtocolVersions = []string{
	LatestProtocolVersion,
	"2025-03-26",
	"2024-11-05",
}

// JSON-RPC 2.0 standard error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000
)

// rpcRequest represents an inbound JSON-RPC 2.0 message.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r *rpcRequest) hasID() bool {
	if len(r.ID) == 0 {
		return false
	}
	s := string(r.ID)
	return s != "null" && s != ""
}

// rpcResponse represents an outbound JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// InitializeParams carries client initialization metadata.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion,omitempty"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ClientInfo      *EntityInfo    `json:"clientInfo,omitempty"`
}

// EntityInfo identifies an MCP client or server implementation.
type EntityInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is returned from the initialize handshake.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      EntityInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerCapabilities describes MCP primitives exposed by Tidegate.
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ToolsCapability indicates tool listing support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// ResourcesCapability indicates MCP resource listing/reading support.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged"`
}

// PromptsCapability indicates MCP prompt listing/retrieval support.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// Resource describes one concrete readable MCP resource URI.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceTemplate describes a parameterized URI template exposed by MCP.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceContents carries textual data returned by resources/read.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// ReadResourceParams holds arguments for resources/read.
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult holds contents returned by resources/read.
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// PromptArgument describes one parameter for a prompt template.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt describes a reusable workflow prompt template.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// GetPromptParams holds arguments for prompts/get.
type GetPromptParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// PromptMessage represents one message returned by prompts/get.
type PromptMessage struct {
	Role    string       `json:"role"`
	Content ContentBlock `json:"content"`
}

// GetPromptResult returns rendered messages for a requested prompt.
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// Tool describes an MCP callable tool and its JSON Schema input contract.
type Tool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations communicates safety hints to MCP hosts per MCP specification.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// CallToolParams holds the arguments for tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ContentBlock represents one content item in an MCP tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult holds the formatted output returned by a tool invocation.
type CallToolResult struct {
	Content           []ContentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

func boolPtr(v bool) *bool { return &v }
