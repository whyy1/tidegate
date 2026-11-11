// Package scenario is the declarative scenario model of Tidegate: the
// definition read from YAML or JSON, its validation, and the compiled,
// immutable execution plan used by the engine.
package scenario

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition is the declarative scenario exchanged between UI, CLI and
// nodes.
type Definition struct {
	Version   int               `yaml:"version" json:"version"`
	Name      string            `yaml:"name" json:"name"`
	Variables map[string]Scalar `yaml:"variables,omitempty" json:"variables,omitempty"`
	HTTP      HTTPOptions       `yaml:"http,omitempty" json:"http,omitempty"`
	Data      []DataSource      `yaml:"data,omitempty" json:"data,omitempty"`
	Steps     []Step            `yaml:"steps" json:"steps"`
	Load      Load              `yaml:"load" json:"load"`
}

// HTTPOptions are connection options. Every field has a default.
type HTTPOptions struct {
	ConnMode           string   `yaml:"conn_mode,omitempty" json:"conn_mode,omitempty"`
	KeepAlive          *bool    `yaml:"keep_alive,omitempty" json:"keep_alive,omitempty"`
	MaxRequestsPerConn int      `yaml:"max_requests_per_conn,omitempty" json:"max_requests_per_conn,omitempty"`
	FollowRedirects    *bool    `yaml:"follow_redirects,omitempty" json:"follow_redirects,omitempty"`
	MaxRedirects       *int     `yaml:"max_redirects,omitempty" json:"max_redirects,omitempty"`
	InsecureSkipVerify bool     `yaml:"insecure_skip_verify,omitempty" json:"insecure_skip_verify,omitempty"`
	Cookies            *bool    `yaml:"cookies,omitempty" json:"cookies,omitempty"`
	Timeout            Timeouts `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxBodyRead        int64    `yaml:"max_body_read,omitempty" json:"max_body_read,omitempty"`
	DNSRefresh         Duration `yaml:"dns_refresh,omitempty" json:"dns_refresh,omitempty"`
}

// Timeouts are the four request timeouts. Zero means the default.
type Timeouts struct {
	Connect   Duration `yaml:"connect,omitempty" json:"connect,omitempty"`
	TLS       Duration `yaml:"tls,omitempty" json:"tls,omitempty"`
	FirstByte Duration `yaml:"first_byte,omitempty" json:"first_byte,omitempty"`
	Total     Duration `yaml:"total,omitempty" json:"total,omitempty"`
}

// DataSource is a data file whose rows feed template references
// {{name.column}}.
type DataSource struct {
	Name string `yaml:"name" json:"name"`
	File string `yaml:"file" json:"file"`
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// Step is one scenario step. Today every step is an HTTP request; later
// versions add other node types (loops, conditions, groups, protocols) as
// sibling fields, so exactly one of them must be set.
type Step struct {
	ID      string    `yaml:"id" json:"id"`
	Name    string    `yaml:"name,omitempty" json:"name,omitempty"`
	Request *Request  `yaml:"request,omitempty" json:"request,omitempty"`
	Extract []Extract `yaml:"extract,omitempty" json:"extract,omitempty"`
	Assert  []Assert  `yaml:"assert,omitempty" json:"assert,omitempty"`
	Think   *Think    `yaml:"think,omitempty" json:"think,omitempty"`
}

// Request is an HTTP request.
type Request struct {
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	URL     string            `yaml:"url" json:"url"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Query   map[string]Scalar `yaml:"query,omitempty" json:"query,omitempty"`
	Body    *Body             `yaml:"body,omitempty" json:"body,omitempty"`
}

// Body is an HTTP request body.
type Body struct {
	Type    string            `yaml:"type" json:"type"`
	Content string            `yaml:"content,omitempty" json:"content,omitempty"`
	Fields  map[string]Scalar `yaml:"fields,omitempty" json:"fields,omitempty"`
	Files   []BodyFile        `yaml:"files,omitempty" json:"files,omitempty"`
}

// BodyFile is a file part of a multipart body.
type BodyFile struct {
	Field       string `yaml:"field" json:"field"`
	Path        string `yaml:"path" json:"path"`
	Filename    string `yaml:"filename,omitempty" json:"filename,omitempty"`
	ContentType string `yaml:"content_type,omitempty" json:"content_type,omitempty"`
}

// Extract stores a value from the response in a variable.
type Extract struct {
	Name    string  `yaml:"name" json:"name"`
	From    string  `yaml:"from" json:"from"`
	Expr    string  `yaml:"expr" json:"expr"`
	Group   *int    `yaml:"group,omitempty" json:"group,omitempty"`
	Default *string `yaml:"default,omitempty" json:"default,omitempty"`
}

// Assert checks the response.
type Assert struct {
	Type     string   `yaml:"type" json:"type"`
	Equals   *Scalar  `yaml:"equals,omitempty" json:"equals,omitempty"`
	In       []int    `yaml:"in,omitempty" json:"in,omitempty"`
	Max      Duration `yaml:"max,omitempty" json:"max,omitempty"`
	Value    string   `yaml:"value,omitempty" json:"value,omitempty"`
	Expr     string   `yaml:"expr,omitempty" json:"expr,omitempty"`
	Exists   *bool    `yaml:"exists,omitempty" json:"exists,omitempty"`
	Contains *string  `yaml:"contains,omitempty" json:"contains,omitempty"`
}

// Think is the pause after a step.
type Think struct {
	Type string   `yaml:"type" json:"type"`
	Min  Duration `yaml:"min,omitempty" json:"min,omitempty"`
	Max  Duration `yaml:"max,omitempty" json:"max,omitempty"`
}

// Load describes the load model.
type Load struct {
	Mode         string    `yaml:"mode" json:"mode"`
	Stages       []Stage   `yaml:"stages" json:"stages"`
	MaxVUs       int       `yaml:"max_vus,omitempty" json:"max_vus,omitempty"`
	MaxRequests  int64     `yaml:"max_requests,omitempty" json:"max_requests,omitempty"`
	GracefulStop *Duration `yaml:"graceful_stop,omitempty" json:"graceful_stop,omitempty"`
}

// Stage ramps linearly from the previous target to Target over Duration.
type Stage struct {
	Duration Duration `yaml:"duration" json:"duration"`
	Target   float64  `yaml:"target" json:"target"`
}

// Duration accepts Go duration strings ("1.5s", "100ms") or plain numbers,
// which are milliseconds.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func parseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if ms, err := strconv.ParseFloat(s, 64); err == nil {
		return Duration(ms * float64(time.Millisecond)), nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("无效的时长 %q（示例：500ms、1.5s、2m）", s)
	}
	return Duration(v), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("第 %d 行：时长必须是字符串或数字", n.Line)
	}
	v, err := parseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("第 %d 行：%v", n.Line, err)
	}
	*d = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	v, err := parseDuration(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Scalar is a string that also accepts numbers and booleans, so that
// `equals: 200` and `port: 8080` work in both YAML and JSON.
type Scalar string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Scalar) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("第 %d 行：这里需要一个标量值（字符串、数字或布尔值）", n.Line)
	}
	if n.Tag == "!!null" {
		*s = ""
		return nil
	}
	*s = Scalar(n.Value)
	return nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Scalar) UnmarshalJSON(b []byte) error {
	str := strings.TrimSpace(string(b))
	switch {
	case str == "null":
		*s = ""
	case strings.HasPrefix(str, `"`):
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = Scalar(v)
	case strings.HasPrefix(str, "{"), strings.HasPrefix(str, "["):
		return fmt.Errorf("这里需要一个标量值（字符串、数字或布尔值）")
	default:
		*s = Scalar(str)
	}
	return nil
}
