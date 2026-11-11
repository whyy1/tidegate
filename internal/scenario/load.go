package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format is a serialization format of a Definition.
type Format uint8

const (
	FormatYAML Format = iota
	FormatJSON
)

// FormatForPath picks the format from a file extension; YAML is the default.
func FormatForPath(path string) Format {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return FormatJSON
	}
	return FormatYAML
}

// maxScenarioSize bounds scenario files; real scenarios are kilobytes.
const maxScenarioSize = 16 << 20

// LoadFile reads a scenario file in YAML or JSON (by extension).
func LoadFile(path string) (*Definition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("无法打开场景文件：%w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScenarioSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取场景文件失败：%w", err)
	}
	if len(data) > maxScenarioSize {
		return nil, fmt.Errorf("场景文件超过 %d MB", maxScenarioSize>>20)
	}
	return Parse(data, FormatForPath(path))
}

// Parse decodes a scenario. Unknown fields are rejected so that typos are
// reported instead of silently ignored.
func Parse(data []byte, format Format) (*Definition, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var def Definition
	switch format {
	case FormatJSON:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&def); err != nil {
			return nil, fmt.Errorf("解析 JSON 失败：%s", jsonErrorText(data, err))
		}
		if dec.More() {
			return nil, errors.New("解析 JSON 失败：文件末尾有多余内容")
		}
	default:
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&def); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("解析 YAML 失败：文件为空")
			}
			return nil, fmt.Errorf("解析 YAML 失败：%s", yamlErrorText(err))
		}
	}
	return &def, nil
}

// Marshal encodes a definition.
func Marshal(def *Definition, format Format) ([]byte, error) {
	if format == FormatJSON {
		return json.MarshalIndent(def, "", "  ")
	}
	return yaml.Marshal(def)
}

var (
	yamlFieldNotFound = regexp.MustCompile(`field (\S+) not found in type \S+`)
	yamlLine          = regexp.MustCompile(`line (\d+): `)
	yamlCannot        = regexp.MustCompile("cannot unmarshal !!(\\w+) `([^`]*)` into (\\S+)")
)

func yamlErrorText(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "yaml: ")
	s = strings.ReplaceAll(s, "unmarshal errors:\n  ", "")
	s = yamlFieldNotFound.ReplaceAllString(s, `未知字段 "$1"`)
	s = yamlCannot.ReplaceAllString(s, "值 `$2` 的类型（$1）不符合要求（$3）")
	s = yamlLine.ReplaceAllString(s, "第 $1 行：")
	return s
}

func jsonErrorText(data []byte, err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Sprintf("第 %d 行：语法错误（%s）", lineOf(data, syn.Offset), syn.Error())
	case errors.As(err, &typ):
		return fmt.Sprintf("第 %d 行：字段 %s 的类型不符合要求（需要 %s，实际是 %s）", lineOf(data, typ.Offset), typ.Field, typ.Type, typ.Value)
	}
	s := err.Error()
	if f, ok := strings.CutPrefix(s, "json: unknown field "); ok {
		return "未知字段 " + f
	}
	if errors.Is(err, io.EOF) {
		return "文件为空"
	}
	return s
}

func lineOf(data []byte, off int64) int {
	if off > int64(len(data)) {
		off = int64(len(data))
	}
	return bytes.Count(data[:off], []byte("\n")) + 1
}
