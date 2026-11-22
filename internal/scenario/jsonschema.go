package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// JSONSchema is a pre-compiled JSON Schema validator for API assertions.
type JSONSchema struct {
	Types                []string
	Required             []string
	Properties           map[string]*JSONSchema
	AdditionalPropsSet   bool
	AdditionalPropsBool  bool
	AdditionalPropsSub   *JSONSchema
	Items                *JSONSchema
	HasEnum              bool
	Enum                 []any
	Min                  *float64
	Max                  *float64
	MinLength            *int
	MaxLength            *int
	MinItems             *int
	MaxItems             *int
	Pattern              *regexp.Regexp
}

// CompileJSONSchema parses and compiles a JSON Schema document.
func CompileJSONSchema(raw string) (*JSONSchema, error) {
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("JSON Schema 格式错误：%v", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON Schema 顶层必须是 JSON 对象")
	}
	return compileSchemaNode("$", obj)
}

func compileSchemaNode(path string, obj map[string]any) (*JSONSchema, error) {
	s := &JSONSchema{}
	if rawType, ok := obj["type"]; ok {
		switch tv := rawType.(type) {
		case string:
			if !validSchemaType(tv) {
				return nil, fmt.Errorf("%s.type 不支持 %q", path, tv)
			}
			s.Types = []string{tv}
		case []any:
			for _, item := range tv {
				str, ok := item.(string)
				if !ok || !validSchemaType(str) {
					return nil, fmt.Errorf("%s.type 列表包含无效类型", path)
				}
				s.Types = append(s.Types, str)
			}
		default:
			return nil, fmt.Errorf("%s.type 必须是字符串或字符串数组", path)
		}
	}
	if reqRaw, ok := obj["required"]; ok {
		arr, ok := reqRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("%s.required 必须是数组", path)
		}
		for _, item := range arr {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s.required 元素必须是字符串", path)
			}
			s.Required = append(s.Required, str)
		}
	}
	if propsRaw, ok := obj["properties"]; ok {
		pmap, ok := propsRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.properties 必须是对象", path)
		}
		s.Properties = make(map[string]*JSONSchema, len(pmap))
		for k, subRaw := range pmap {
			subObj, ok := subRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.properties.%s 必须是对象", path, k)
			}
			sub, err := compileSchemaNode(path+".properties."+k, subObj)
			if err != nil {
				return nil, err
			}
			s.Properties[k] = sub
		}
	}
	if apRaw, ok := obj["additionalProperties"]; ok {
		s.AdditionalPropsSet = true
		switch av := apRaw.(type) {
		case bool:
			s.AdditionalPropsBool = av
		case map[string]any:
			sub, err := compileSchemaNode(path+".additionalProperties", av)
			if err != nil {
				return nil, err
			}
			s.AdditionalPropsSub = sub
		default:
			return nil, fmt.Errorf("%s.additionalProperties 必须是布尔值或 Schema 对象", path)
		}
	}
	if itemsRaw, ok := obj["items"]; ok {
		subObj, ok := itemsRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.items 必须是 Schema 对象", path)
		}
		sub, err := compileSchemaNode(path+".items", subObj)
		if err != nil {
			return nil, err
		}
		s.Items = sub
	}
	if enumRaw, ok := obj["enum"]; ok {
		arr, ok := enumRaw.([]any)
		if !ok || len(arr) == 0 {
			return nil, fmt.Errorf("%s.enum 必须是非空数组", path)
		}
		s.HasEnum = true
		s.Enum = arr
	}
	if v, ok, err := schemaFloat(obj, path, "minimum"); err != nil {
		return nil, err
	} else if ok {
		s.Min = &v
	}
	if v, ok, err := schemaFloat(obj, path, "maximum"); err != nil {
		return nil, err
	} else if ok {
		s.Max = &v
	}
	if v, ok, err := schemaInt(obj, path, "minLength"); err != nil {
		return nil, err
	} else if ok {
		s.MinLength = &v
	}
	if v, ok, err := schemaInt(obj, path, "maxLength"); err != nil {
		return nil, err
	} else if ok {
		s.MaxLength = &v
	}
	if v, ok, err := schemaInt(obj, path, "minItems"); err != nil {
		return nil, err
	} else if ok {
		s.MinItems = &v
	}
	if v, ok, err := schemaInt(obj, path, "maxItems"); err != nil {
		return nil, err
	} else if ok {
		s.MaxItems = &v
	}
	if patRaw, ok := obj["pattern"]; ok {
		pat, ok := patRaw.(string)
		if !ok {
			return nil, fmt.Errorf("%s.pattern 必须是字符串", path)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("%s.pattern 正则无效：%v", path, err)
		}
		s.Pattern = re
	}
	return s, nil
}

func validSchemaType(t string) bool {
	switch t {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	}
	return false
}

func schemaFloat(obj map[string]any, path, key string) (float64, bool, error) {
	raw, ok := obj[key]
	if !ok {
		return 0, false, nil
	}
	n, ok := raw.(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("%s.%s 必须是数字", path, key)
	}
	f, err := n.Float64()
	if err != nil {
		return 0, false, fmt.Errorf("%s.%s 数字无效：%v", path, key, err)
	}
	return f, true, nil
}

func schemaInt(obj map[string]any, path, key string) (int, bool, error) {
	f, ok, err := schemaFloat(obj, path, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	if f < 0 || math.Trunc(f) != f {
		return 0, false, fmt.Errorf("%s.%s 必须是非负整数", path, key)
	}
	return int(f), true, nil
}

// Validate checks a JSON response payload against the compiled schema.
func (s *JSONSchema) Validate(body []byte) (bool, string) {
	if s == nil {
		return true, ""
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return false, fmt.Sprintf("响应不是合法 JSON：%v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return false, "响应包含多余的 JSON 内容"
	}
	if msg := s.validateValue("$", val); msg != "" {
		return false, msg
	}
	return true, ""
}

func (s *JSONSchema) validateValue(path string, val any) string {
	if len(s.Types) > 0 {
		matched := false
		for _, want := range s.Types {
			if valueMatchesType(val, want) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Sprintf("%s 类型不匹配，期望 %v，实际 %s", path, s.Types, actualJSONType(val))
		}
	}
	if s.HasEnum {
		matched := false
		for _, ev := range s.Enum {
			if jsonAnyEqual(val, ev) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Sprintf("%s 值不在允许的枚举范围内", path)
		}
	}

	switch v := val.(type) {
	case map[string]any:
		for _, reqKey := range s.Required {
			if _, exists := v[reqKey]; !exists {
				return fmt.Sprintf("%s 缺少必需字段 %q", path, reqKey)
			}
		}
		for k, fieldVal := range v {
			if sub, defined := s.Properties[k]; defined {
				if msg := sub.validateValue(path+"."+k, fieldVal); msg != "" {
					return msg
				}
				continue
			}
			if s.AdditionalPropsSet {
				if s.AdditionalPropsSub != nil {
					if msg := s.AdditionalPropsSub.validateValue(path+"."+k, fieldVal); msg != "" {
						return msg
					}
				} else if !s.AdditionalPropsBool {
					return fmt.Sprintf("%s 不允许额外字段 %q", path, k)
				}
			}
		}
	case []any:
		if s.MinItems != nil && len(v) < *s.MinItems {
			return fmt.Sprintf("%s 元素数 %d 小于 minItems %d", path, len(v), *s.MinItems)
		}
		if s.MaxItems != nil && len(v) > *s.MaxItems {
			return fmt.Sprintf("%s 元素数 %d 大于 maxItems %d", path, len(v), *s.MaxItems)
		}
		if s.Items != nil {
			for i, item := range v {
				if msg := s.Items.validateValue(path+"["+strconv.Itoa(i)+"]", item); msg != "" {
					return msg
				}
			}
		}
	case string:
		runeCount := utf8.RuneCountInString(v)
		if s.MinLength != nil && runeCount < *s.MinLength {
			return fmt.Sprintf("%s 长度 %d 小于 minLength %d", path, runeCount, *s.MinLength)
		}
		if s.MaxLength != nil && runeCount > *s.MaxLength {
			return fmt.Sprintf("%s 长度 %d 大于 maxLength %d", path, runeCount, *s.MaxLength)
		}
		if s.Pattern != nil && !s.Pattern.MatchString(v) {
			return fmt.Sprintf("%s 不匹配正则 %q", path, s.Pattern.String())
		}
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return fmt.Sprintf("%s 数字无法解析", path)
		}
		if s.Min != nil && f < *s.Min {
			return fmt.Sprintf("%s 值 %v 小于 minimum %v", path, v.String(), *s.Min)
		}
		if s.Max != nil && f > *s.Max {
			return fmt.Sprintf("%s 值 %v 大于 maximum %v", path, v.String(), *s.Max)
		}
	}
	return ""
}

func valueMatchesType(val any, want string) bool {
	switch v := val.(type) {
	case nil:
		return want == "null"
	case bool:
		return want == "boolean"
	case string:
		return want == "string"
	case []any:
		return want == "array"
	case map[string]any:
		return want == "object"
	case json.Number:
		if want == "number" {
			return true
		}
		if want == "integer" {
			f, err := v.Float64()
			return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && math.Trunc(f) == f
		}
	}
	return false
}

func actualJSONType(val any) string {
	switch val.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", val)
	}
}

func jsonAnyEqual(a, b any) bool {
	if an, ok := a.(json.Number); ok {
		if bn, ok := b.(json.Number); ok {
			af, err1 := an.Float64()
			bf, err2 := bn.Float64()
			return err1 == nil && err2 == nil && af == bf
		}
		return false
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}
