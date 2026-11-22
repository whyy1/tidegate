package grpcproto

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// MethodInfo describes a single RPC method discovered from reflection or a .proto file.
type MethodInfo struct {
	Name            string `json:"name"`
	FullMethod      string `json:"full_method"`
	InputType       string `json:"input_type"`
	OutputType      string `json:"output_type"`
	ClientStreaming bool   `json:"client_streaming,omitempty"`
	ServerStreaming bool   `json:"server_streaming,omitempty"`
	SampleJSON      string `json:"sample_json,omitempty"`
}

// ServiceInfo describes a gRPC service and its methods.
type ServiceInfo struct {
	Name    string       `json:"name"`
	Methods []MethodInfo `json:"methods"`
}

// SchemaCatalog holds discovered gRPC services and methods.
type SchemaCatalog struct {
	Services []ServiceInfo `json:"services"`
}

// BuildCatalog extracts all user services and unary/streaming method schemas from files.
func BuildCatalog(files *protoregistry.Files) *SchemaCatalog {
	cat := &SchemaCatalog{Services: []ServiceInfo{}}
	if files == nil {
		return cat
	}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			sd := svcs.Get(i)
			fullSvc := string(sd.FullName())
			if strings.HasPrefix(fullSvc, "grpc.reflection.") {
				continue
			}
			si := ServiceInfo{
				Name:    fullSvc,
				Methods: make([]MethodInfo, 0, sd.Methods().Len()),
			}
			ms := sd.Methods()
			for j := 0; j < ms.Len(); j++ {
				md := ms.Get(j)
				si.Methods = append(si.Methods, MethodInfo{
					Name:            string(md.Name()),
					FullMethod:      "/" + fullSvc + "/" + string(md.Name()),
					InputType:       string(md.Input().FullName()),
					OutputType:      string(md.Output().FullName()),
					ClientStreaming: md.IsStreamingClient(),
					ServerStreaming: md.IsStreamingServer(),
					SampleJSON:      SampleJSON(md.Input()),
				})
			}
			sort.Slice(si.Methods, func(a, b int) bool { return si.Methods[a].Name < si.Methods[b].Name })
			cat.Services = append(cat.Services, si)
		}
		return true
	})
	sort.Slice(cat.Services, func(a, b int) bool { return cat.Services[a].Name < cat.Services[b].Name })
	return cat
}

// ParseProtoFiles builds a protoregistry from either a binary FileDescriptorSet
// or source .proto files keyed by relative filename.
func ParseProtoFiles(entryName string, files map[string][]byte) (*protoregistry.Files, error) {
	raw, ok := files[entryName]
	if !ok {
		return nil, fmt.Errorf("未找到 Proto 描述文件 %q", entryName)
	}
	// Detect binary FileDescriptorSet (.pb / .desc).
	if strings.HasSuffix(strings.ToLower(entryName), ".pb") ||
		strings.HasSuffix(strings.ToLower(entryName), ".desc") ||
		bytes.IndexByte(raw, 0) >= 0 ||
		!utf8.Valid(raw) {
		var fds descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(raw, &fds); err != nil {
			return nil, fmt.Errorf("解析二进制 FileDescriptorSet 失败：%w", err)
		}
		if len(fds.File) == 0 {
			return nil, errors.New("二进制 FileDescriptorSet 不包含任何文件描述")
		}
		return protodesc.NewFiles(&fds)
	}

	parsed := make(map[string]*descriptorpb.FileDescriptorProto, len(files))
	seen := make(map[string]bool)
	var parseOne func(name string) error
	parseOne = func(name string) error {
		if seen[name] {
			return nil
		}
		if wk := wellKnownDescriptor(name); wk != nil {
			seen[name] = true
			parsed[name] = wk
			for _, dep := range wk.Dependency {
				if err := parseOne(dep); err != nil {
					return err
				}
			}
			return nil
		}
		content, exists := files[name]
		if !exists {
			return fmt.Errorf("找不到引用的 Proto 文件 %q", name)
		}
		seen[name] = true
		fdp, err := parseProtoText(name, string(content))
		if err != nil {
			return fmt.Errorf("解析 %s 失败：%w", name, err)
		}
		parsed[name] = fdp
		for _, imp := range fdp.Dependency {
			if err := parseOne(imp); err != nil {
				return err
			}
		}
		return nil
	}

	if err := parseOne(entryName); err != nil {
		return nil, err
	}

	if err := resolveAllTypes(parsed); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(parsed))
	for k := range parsed {
		names = append(names, k)
	}
	sort.Strings(names)
	fds := &descriptorpb.FileDescriptorSet{
		File: make([]*descriptorpb.FileDescriptorProto, 0, len(names)),
	}
	for _, n := range names {
		fds.File = append(fds.File, parsed[n])
	}
	return protodesc.NewFiles(fds)
}

func wellKnownDescriptor(name string) *descriptorpb.FileDescriptorProto {
	switch name {
	case "google/protobuf/empty.proto":
		return protodesc.ToFileDescriptorProto(emptypb.File_google_protobuf_empty_proto)
	case "google/protobuf/timestamp.proto":
		return protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto)
	case "google/protobuf/duration.proto":
		return protodesc.ToFileDescriptorProto(durationpb.File_google_protobuf_duration_proto)
	case "google/protobuf/wrappers.proto":
		return protodesc.ToFileDescriptorProto(wrapperspb.File_google_protobuf_wrappers_proto)
	case "google/protobuf/any.proto":
		return protodesc.ToFileDescriptorProto(anypb.File_google_protobuf_any_proto)
	case "google/protobuf/struct.proto":
		return protodesc.ToFileDescriptorProto(structpb.File_google_protobuf_struct_proto)
	case "google/protobuf/field_mask.proto":
		return protodesc.ToFileDescriptorProto(fieldmaskpb.File_google_protobuf_field_mask_proto)
	default:
		return nil
	}
}

var scalarTypeMap = map[string]descriptorpb.FieldDescriptorProto_Type{
	"double":   descriptorpb.FieldDescriptorProto_TYPE_DOUBLE,
	"float":    descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
	"int64":    descriptorpb.FieldDescriptorProto_TYPE_INT64,
	"uint64":   descriptorpb.FieldDescriptorProto_TYPE_UINT64,
	"int32":    descriptorpb.FieldDescriptorProto_TYPE_INT32,
	"fixed64":  descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
	"fixed32":  descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
	"bool":     descriptorpb.FieldDescriptorProto_TYPE_BOOL,
	"string":   descriptorpb.FieldDescriptorProto_TYPE_STRING,
	"bytes":    descriptorpb.FieldDescriptorProto_TYPE_BYTES,
	"uint32":   descriptorpb.FieldDescriptorProto_TYPE_UINT32,
	"sfixed32": descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
	"sfixed64": descriptorpb.FieldDescriptorProto_TYPE_SFIXED64,
	"sint32":   descriptorpb.FieldDescriptorProto_TYPE_SINT32,
	"sint64":   descriptorpb.FieldDescriptorProto_TYPE_SINT64,
}

type protoTokKind int

const (
	tokEOF protoTokKind = iota
	tokIdent
	tokString
	tokNumber
	tokSym
)

type protoTok struct {
	kind protoTokKind
	val  string
	line int
}

type protoParser struct {
	src  string
	pos  int
	line int
	peek *protoTok
}

func newProtoParser(src string) *protoParser {
	return &protoParser{src: src, line: 1}
}

func (p *protoParser) skipWSAndComments() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\n' {
			p.line++
			p.pos++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			p.pos++
			continue
		}
		if strings.HasPrefix(p.src[p.pos:], "//") {
			p.pos += 2
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		if strings.HasPrefix(p.src[p.pos:], "/*") {
			p.pos += 2
			for p.pos < len(p.src) {
				if p.src[p.pos] == '\n' {
					p.line++
				}
				if strings.HasPrefix(p.src[p.pos:], "*/") {
					p.pos += 2
					break
				}
				p.pos++
			}
			continue
		}
		break
	}
}

func (p *protoParser) next() (protoTok, error) {
	if p.peek != nil {
		t := *p.peek
		p.peek = nil
		return t, nil
	}
	p.skipWSAndComments()
	if p.pos >= len(p.src) {
		return protoTok{kind: tokEOF, line: p.line}, nil
	}
	line := p.line
	c := p.src[p.pos]
	if c == '"' || c == '\'' {
		quote := c
		p.pos++
		var b strings.Builder
		for p.pos < len(p.src) && p.src[p.pos] != quote {
			if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) {
				p.pos++
				switch p.src[p.pos] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default:
					b.WriteByte(p.src[p.pos])
				}
				p.pos++
				continue
			}
			if p.src[p.pos] == '\n' {
				p.line++
			}
			b.WriteByte(p.src[p.pos])
			p.pos++
		}
		if p.pos >= len(p.src) {
			return protoTok{}, fmt.Errorf("第 %d 行：字符串未闭合", line)
		}
		p.pos++
		return protoTok{kind: tokString, val: b.String(), line: line}, nil
	}
	if isIdentStart(c) {
		start := p.pos
		for p.pos < len(p.src) {
			ch := p.src[p.pos]
			if isIdentPart(ch) || ch == '.' {
				p.pos++
			} else {
				break
			}
		}
		return protoTok{kind: tokIdent, val: p.src[start:p.pos], line: line}, nil
	}
	if (c >= '0' && c <= '9') || (c == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9') {
		start := p.pos
		p.pos++
		for p.pos < len(p.src) && (isIdentPart(p.src[p.pos]) || p.src[p.pos] == '.') {
			p.pos++
		}
		return protoTok{kind: tokNumber, val: p.src[start:p.pos], line: line}, nil
	}
	if c == '.' && p.pos+1 < len(p.src) && isIdentStart(p.src[p.pos+1]) {
		start := p.pos
		p.pos++
		for p.pos < len(p.src) && (isIdentPart(p.src[p.pos]) || p.src[p.pos] == '.') {
			p.pos++
		}
		return protoTok{kind: tokIdent, val: p.src[start:p.pos], line: line}, nil
	}
	p.pos++
	return protoTok{kind: tokSym, val: string(c), line: line}, nil
}

func (p *protoParser) peekTok() (protoTok, error) {
	if p.peek == nil {
		t, err := p.next()
		if err != nil {
			return protoTok{}, err
		}
		p.peek = &t
	}
	return *p.peek, nil
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (p *protoParser) expectSym(sym string) error {
	t, err := p.next()
	if err != nil {
		return err
	}
	if t.kind != tokSym || t.val != sym {
		return fmt.Errorf("第 %d 行：期望 %q，实际得到 %q", t.line, sym, t.val)
	}
	return nil
}

func (p *protoParser) skipStatement() error {
	depthBrace := 0
	depthBrack := 0
	for {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return nil
		}
		if t.kind == tokSym {
			switch t.val {
			case "{":
				depthBrace++
			case "}":
				if depthBrace > 0 {
					depthBrace--
					if depthBrace == 0 && depthBrack == 0 {
						// Consume optional trailing ';' if present.
						if nt, _ := p.peekTok(); nt.kind == tokSym && nt.val == ";" {
							_, _ = p.next()
						}
						return nil
					}
				}
			case "[":
				depthBrack++
			case "]":
				if depthBrack > 0 {
					depthBrack--
				}
			case ";":
				if depthBrace == 0 && depthBrack == 0 {
					return nil
				}
			}
		}
	}
}

func (p *protoParser) skipBrackets() error {
	nt, err := p.peekTok()
	if err != nil || nt.kind != tokSym || nt.val != "[" {
		return err
	}
	_, _ = p.next()
	depth := 1
	for depth > 0 {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokEOF {
			return fmt.Errorf("第 %d 行：选项 [...] 未闭合", t.line)
		}
		if t.kind == tokSym {
			if t.val == "[" {
				depth++
			} else if t.val == "]" {
				depth--
			}
		}
	}
	return nil
}

func parseProtoText(filename, src string) (*descriptorpb.FileDescriptorProto, error) {
	p := newProtoParser(src)
	fdp := &descriptorpb.FileDescriptorProto{
		Name:   proto.String(filename),
		Syntax: proto.String("proto3"),
	}
	for {
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tokEOF {
			break
		}
		if t.kind == tokSym && t.val == ";" {
			continue
		}
		if t.kind != tokIdent {
			return nil, fmt.Errorf("第 %d 行：意外记号 %q", t.line, t.val)
		}
		switch t.val {
		case "syntax":
			if err := p.expectSym("="); err != nil {
				return nil, err
			}
			v, err := p.next()
			if err != nil {
				return nil, err
			}
			if v.kind != tokString {
				return nil, fmt.Errorf("第 %d 行：syntax 必须是字符串", v.line)
			}
			fdp.Syntax = proto.String(v.val)
			if err := p.expectSym(";"); err != nil {
				return nil, err
			}
		case "package":
			v, err := p.next()
			if err != nil {
				return nil, err
			}
			if v.kind != tokIdent {
				return nil, fmt.Errorf("第 %d 行：package 名称无效", v.line)
			}
			fdp.Package = proto.String(v.val)
			if err := p.expectSym(";"); err != nil {
				return nil, err
			}
		case "import":
			nt, err := p.peekTok()
			if err != nil {
				return nil, err
			}
			if nt.kind == tokIdent && (nt.val == "public" || nt.val == "weak") {
				_, _ = p.next()
			}
			v, err := p.next()
			if err != nil {
				return nil, err
			}
			if v.kind != tokString {
				return nil, fmt.Errorf("第 %d 行：import 需要字符串路径", v.line)
			}
			fdp.Dependency = append(fdp.Dependency, v.val)
			if err := p.expectSym(";"); err != nil {
				return nil, err
			}
		case "option":
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
		case "message":
			msg, err := p.parseMessage(fdp.GetSyntax() == "proto3")
			if err != nil {
				return nil, err
			}
			fdp.MessageType = append(fdp.MessageType, msg)
		case "enum":
			en, err := p.parseEnum()
			if err != nil {
				return nil, err
			}
			fdp.EnumType = append(fdp.EnumType, en)
		case "service":
			svc, err := p.parseService()
			if err != nil {
				return nil, err
			}
			fdp.Service = append(fdp.Service, svc)
		default:
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
		}
	}
	return fdp, nil
}

func (p *protoParser) parseEnum() (*descriptorpb.EnumDescriptorProto, error) {
	nameTok, err := p.next()
	if err != nil {
		return nil, err
	}
	if nameTok.kind != tokIdent {
		return nil, fmt.Errorf("第 %d 行：枚举名无效", nameTok.line)
	}
	if err := p.expectSym("{"); err != nil {
		return nil, err
	}
	ed := &descriptorpb.EnumDescriptorProto{Name: proto.String(nameTok.val)}
	for {
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tokEOF {
			return nil, fmt.Errorf("第 %d 行：enum %s 未闭合", t.line, nameTok.val)
		}
		if t.kind == tokSym && t.val == "}" {
			return ed, nil
		}
		if t.kind == tokSym && t.val == ";" {
			continue
		}
		if t.kind == tokIdent && (t.val == "option" || t.val == "reserved") {
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
			continue
		}
		if t.kind != tokIdent {
			return nil, fmt.Errorf("第 %d 行：枚举项名无效 %q", t.line, t.val)
		}
		if err := p.expectSym("="); err != nil {
			return nil, err
		}
		numTok, err := p.next()
		if err != nil {
			return nil, err
		}
		n, perr := strconv.ParseInt(numTok.val, 0, 32)
		if perr != nil {
			return nil, fmt.Errorf("第 %d 行：枚举值 %q 无效", numTok.line, numTok.val)
		}
		if err := p.skipBrackets(); err != nil {
			return nil, err
		}
		if err := p.expectSym(";"); err != nil {
			return nil, err
		}
		num32 := int32(n)
		ed.Value = append(ed.Value, &descriptorpb.EnumValueDescriptorProto{
			Name:   proto.String(t.val),
			Number: &num32,
		})
	}
}

func (p *protoParser) parseMessage(isProto3 bool) (*descriptorpb.DescriptorProto, error) {
	nameTok, err := p.next()
	if err != nil {
		return nil, err
	}
	if nameTok.kind != tokIdent {
		return nil, fmt.Errorf("第 %d 行：message 名称无效", nameTok.line)
	}
	if err := p.expectSym("{"); err != nil {
		return nil, err
	}
	msg := &descriptorpb.DescriptorProto{Name: proto.String(nameTok.val)}
	for {
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tokEOF {
			return nil, fmt.Errorf("第 %d 行：message %s 未闭合", t.line, nameTok.val)
		}
		if t.kind == tokSym && t.val == "}" {
			return msg, nil
		}
		if t.kind == tokSym && t.val == ";" {
			continue
		}
		if t.kind != tokIdent {
			return nil, fmt.Errorf("第 %d 行：message %s 中意外记号 %q", t.line, nameTok.val, t.val)
		}
		switch t.val {
		case "option", "reserved", "extensions":
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
		case "message":
			nested, err := p.parseMessage(isProto3)
			if err != nil {
				return nil, err
			}
			msg.NestedType = append(msg.NestedType, nested)
		case "enum":
			en, err := p.parseEnum()
			if err != nil {
				return nil, err
			}
			msg.EnumType = append(msg.EnumType, en)
		case "oneof":
			if err := p.parseOneof(msg, isProto3); err != nil {
				return nil, err
			}
		case "map":
			if err := p.parseMapField(msg); err != nil {
				return nil, err
			}
		default:
			if err := p.parseField(msg, t, isProto3, nil); err != nil {
				return nil, err
			}
		}
	}
}

func (p *protoParser) parseOneof(msg *descriptorpb.DescriptorProto, isProto3 bool) error {
	nameTok, err := p.next()
	if err != nil {
		return err
	}
	if nameTok.kind != tokIdent {
		return fmt.Errorf("第 %d 行：oneof 名称无效", nameTok.line)
	}
	if err := p.expectSym("{"); err != nil {
		return err
	}
	idx := int32(len(msg.OneofDecl))
	msg.OneofDecl = append(msg.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String(nameTok.val)})
	for {
		t, err := p.next()
		if err != nil {
			return err
		}
		if t.kind == tokSym && t.val == "}" {
			return nil
		}
		if t.kind == tokSym && t.val == ";" {
			continue
		}
		if t.kind != tokIdent {
			return fmt.Errorf("第 %d 行：oneof 字段无效 %q", t.line, t.val)
		}
		if t.val == "option" {
			if err := p.skipStatement(); err != nil {
				return err
			}
			continue
		}
		if err := p.parseField(msg, t, isProto3, &idx); err != nil {
			return err
		}
	}
}

func (p *protoParser) parseMapField(msg *descriptorpb.DescriptorProto) error {
	if err := p.expectSym("<"); err != nil {
		return err
	}
	keyTok, err := p.next()
	if err != nil {
		return err
	}
	if err := p.expectSym(","); err != nil {
		return err
	}
	valTok, err := p.next()
	if err != nil {
		return err
	}
	if err := p.expectSym(">"); err != nil {
		return err
	}
	nameTok, err := p.next()
	if err != nil {
		return err
	}
	if err := p.expectSym("="); err != nil {
		return err
	}
	numTok, err := p.next()
	if err != nil {
		return err
	}
	n, perr := strconv.ParseInt(numTok.val, 0, 32)
	if perr != nil {
		return fmt.Errorf("第 %d 行：map 字段编号无效 %q", numTok.line, numTok.val)
	}
	if err := p.skipBrackets(); err != nil {
		return err
	}
	if err := p.expectSym(";"); err != nil {
		return err
	}

	entryName := mapEntryTypeName(nameTok.val)
	optLabel := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repLabel := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	msgType := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE

	kField := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("key"),
		JsonName: proto.String("key"),
		Number:   proto.Int32(1),
		Label:    &optLabel,
	}
	setFieldType(kField, keyTok.val)
	vField := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("value"),
		JsonName: proto.String("value"),
		Number:   proto.Int32(2),
		Label:    &optLabel,
	}
	setFieldType(vField, valTok.val)

	entry := &descriptorpb.DescriptorProto{
		Name:    proto.String(entryName),
		Field:   []*descriptorpb.FieldDescriptorProto{kField, vField},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
	msg.NestedType = append(msg.NestedType, entry)

	num32 := int32(n)
	jn := lowerCamel(nameTok.val)
	msg.Field = append(msg.Field, &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(nameTok.val),
		JsonName: proto.String(jn),
		Number:   &num32,
		Label:    &repLabel,
		Type:     &msgType,
		TypeName: proto.String(entryName),
	})
	return nil
}

func (p *protoParser) parseField(msg *descriptorpb.DescriptorProto, first protoTok, isProto3 bool, oneofIdx *int32) error {
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	typeTok := first
	proto3Opt := false
	if oneofIdx == nil && (first.val == "repeated" || first.val == "optional" || first.val == "required") {
		switch first.val {
		case "repeated":
			label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		case "required":
			label = descriptorpb.FieldDescriptorProto_LABEL_REQUIRED
		case "optional":
			label = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
			if isProto3 {
				proto3Opt = true
			}
		}
		var err error
		typeTok, err = p.next()
		if err != nil {
			return err
		}
	}
	nameTok, err := p.next()
	if err != nil {
		return err
	}
	if nameTok.kind != tokIdent {
		return fmt.Errorf("第 %d 行：字段名无效 %q", nameTok.line, nameTok.val)
	}
	if err := p.expectSym("="); err != nil {
		return err
	}
	numTok, err := p.next()
	if err != nil {
		return err
	}
	n, perr := strconv.ParseInt(numTok.val, 0, 32)
	if perr != nil {
		return fmt.Errorf("第 %d 行：字段编号无效 %q", numTok.line, numTok.val)
	}
	if err := p.skipBrackets(); err != nil {
		return err
	}
	if err := p.expectSym(";"); err != nil {
		return err
	}
	num32 := int32(n)
	jn := lowerCamel(nameTok.val)
	fd := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(nameTok.val),
		JsonName: proto.String(jn),
		Number:   &num32,
		Label:    &label,
	}
	setFieldType(fd, typeTok.val)
	if oneofIdx != nil {
		fd.OneofIndex = oneofIdx
	} else if proto3Opt {
		idx := int32(len(msg.OneofDecl))
		msg.OneofDecl = append(msg.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("_" + nameTok.val)})
		fd.OneofIndex = &idx
		fd.Proto3Optional = proto.Bool(true)
	}
	msg.Field = append(msg.Field, fd)
	return nil
}

func setFieldType(fd *descriptorpb.FieldDescriptorProto, typeName string) {
	if t, ok := scalarTypeMap[typeName]; ok {
		fd.Type = &t
		return
	}
	// Deferred to resolveAllTypes to distinguish message vs enum and normalize FQN.
	fd.TypeName = proto.String(typeName)
}

func (p *protoParser) parseService() (*descriptorpb.ServiceDescriptorProto, error) {
	nameTok, err := p.next()
	if err != nil {
		return nil, err
	}
	if nameTok.kind != tokIdent {
		return nil, fmt.Errorf("第 %d 行：service 名称无效", nameTok.line)
	}
	if err := p.expectSym("{"); err != nil {
		return nil, err
	}
	sd := &descriptorpb.ServiceDescriptorProto{Name: proto.String(nameTok.val)}
	for {
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		if t.kind == tokEOF {
			return nil, fmt.Errorf("第 %d 行：service %s 未闭合", t.line, nameTok.val)
		}
		if t.kind == tokSym && t.val == "}" {
			return sd, nil
		}
		if t.kind == tokSym && t.val == ";" {
			continue
		}
		if t.kind != tokIdent {
			return nil, fmt.Errorf("第 %d 行：service %s 意外记号 %q", t.line, nameTok.val, t.val)
		}
		if t.val == "option" {
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
			continue
		}
		if t.val != "rpc" {
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
			continue
		}
		mTok, err := p.next()
		if err != nil {
			return nil, err
		}
		if err := p.expectSym("("); err != nil {
			return nil, err
		}
		inStream := false
		inTok, err := p.next()
		if err != nil {
			return nil, err
		}
		if inTok.kind == tokIdent && inTok.val == "stream" {
			inStream = true
			inTok, err = p.next()
			if err != nil {
				return nil, err
			}
		}
		if err := p.expectSym(")"); err != nil {
			return nil, err
		}
		retTok, err := p.next()
		if err != nil || retTok.kind != tokIdent || retTok.val != "returns" {
			return nil, fmt.Errorf("第 %d 行：rpc %s 缺少 returns", mTok.line, mTok.val)
		}
		if err := p.expectSym("("); err != nil {
			return nil, err
		}
		outStream := false
		outTok, err := p.next()
		if err != nil {
			return nil, err
		}
		if outTok.kind == tokIdent && outTok.val == "stream" {
			outStream = true
			outTok, err = p.next()
			if err != nil {
				return nil, err
			}
		}
		if err := p.expectSym(")"); err != nil {
			return nil, err
		}
		nt, err := p.peekTok()
		if err != nil {
			return nil, err
		}
		if nt.kind == tokSym && (nt.val == "{" || nt.val == ";") {
			if err := p.skipStatement(); err != nil {
				return nil, err
			}
		}
		md := &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(mTok.val),
			InputType:  proto.String(inTok.val),
			OutputType: proto.String(outTok.val),
		}
		if inStream {
			md.ClientStreaming = proto.Bool(true)
		}
		if outStream {
			md.ServerStreaming = proto.Bool(true)
		}
		sd.Method = append(sd.Method, md)
	}
}

func resolveAllTypes(parsed map[string]*descriptorpb.FileDescriptorProto) error {
	type symKind int
	const (
		symMsg symKind = iota + 1
		symEnum
	)
	symbols := make(map[string]symKind)
	var indexMsg func(scope string, m *descriptorpb.DescriptorProto)
	indexMsg = func(scope string, m *descriptorpb.DescriptorProto) {
		full := joinScope(scope, m.GetName())
		symbols[full] = symMsg
		for _, en := range m.EnumType {
			symbols[joinScope(full, en.GetName())] = symEnum
		}
		for _, nested := range m.NestedType {
			indexMsg(full, nested)
		}
	}
	for _, f := range parsed {
		pkg := f.GetPackage()
		for _, en := range f.EnumType {
			symbols[joinScope(pkg, en.GetName())] = symEnum
		}
		for _, m := range f.MessageType {
			indexMsg(pkg, m)
		}
	}

	resolveSym := func(curScope, raw string) (string, symKind, error) {
		if strings.HasPrefix(raw, ".") {
			fq := strings.TrimPrefix(raw, ".")
			if k, ok := symbols[fq]; ok {
				return "." + fq, k, nil
			}
			return "", 0, fmt.Errorf("未定义类型 %q", raw)
		}
		scope := curScope
		for {
			cand := joinScope(scope, raw)
			if k, ok := symbols[cand]; ok {
				return "." + cand, k, nil
			}
			if scope == "" {
				break
			}
			if dot := strings.LastIndexByte(scope, '.'); dot >= 0 {
				scope = scope[:dot]
			} else {
				scope = ""
			}
		}
		return "", 0, fmt.Errorf("无法解析类型 %q（作用域 %q）", raw, curScope)
	}

	var resolveMsg func(pkg, scope string, m *descriptorpb.DescriptorProto) error
	resolveMsg = func(pkg, scope string, m *descriptorpb.DescriptorProto) error {
		curScope := joinScope(scope, m.GetName())
		for _, fd := range m.Field {
			if fd.Type != nil {
				continue
			}
			fq, kind, err := resolveSym(curScope, fd.GetTypeName())
			if err != nil {
				return err
			}
			fd.TypeName = &fq
			if kind == symEnum {
				t := descriptorpb.FieldDescriptorProto_TYPE_ENUM
				fd.Type = &t
			} else {
				t := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
				fd.Type = &t
			}
		}
		for _, nested := range m.NestedType {
			if err := resolveMsg(pkg, curScope, nested); err != nil {
				return err
			}
		}
		return nil
	}

	for _, f := range parsed {
		pkg := f.GetPackage()
		for _, m := range f.MessageType {
			if err := resolveMsg(pkg, pkg, m); err != nil {
				return err
			}
		}
		for _, svc := range f.Service {
			for _, m := range svc.Method {
				inFQ, inKind, err := resolveSym(pkg, m.GetInputType())
				if err != nil || inKind != symMsg {
					return fmt.Errorf("rpc %s 输入类型无效：%w", m.GetName(), err)
				}
				m.InputType = &inFQ
				outFQ, outKind, err := resolveSym(pkg, m.GetOutputType())
				if err != nil || outKind != symMsg {
					return fmt.Errorf("rpc %s 输出类型无效：%w", m.GetName(), err)
				}
				m.OutputType = &outFQ
			}
		}
	}
	return nil
}

func joinScope(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + "." + name
}

func mapEntryTypeName(fieldName string) string {
	var b strings.Builder
	upper := true
	for _, r := range fieldName {
		if r == '_' {
			upper = true
			continue
		}
		if upper {
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteString("Entry")
	return b.String()
}

func lowerCamel(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	upper := false
	for i, r := range s {
		if r == '_' {
			upper = true
			continue
		}
		if i == 0 {
			b.WriteRune(unicode.ToLower(r))
			upper = false
			continue
		}
		if upper {
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
