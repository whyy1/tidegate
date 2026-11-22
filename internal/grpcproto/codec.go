package grpcproto

import (
	"bytes"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// MethodCodec translates between JSON text and protobuf wire messages for one RPC method.
type MethodCodec struct {
	FullMethod string
	InDesc     protoreflect.MessageDescriptor
	OutDesc    protoreflect.MessageDescriptor
}

// NewMethodCodec locates fullMethod ("/pkg.Service/Method" or "pkg.Service/Method")
// inside files and returns a codec bound to its input and output message descriptors.
func NewMethodCodec(files *protoregistry.Files, fullMethod string) (*MethodCodec, error) {
	if files == nil {
		return nil, fmt.Errorf("未加载 gRPC 描述集")
	}
	trimmed := strings.Trim(strings.TrimSpace(fullMethod), "/")
	slash := strings.LastIndexByte(trimmed, '/')
	if slash <= 0 || slash == len(trimmed)-1 {
		return nil, fmt.Errorf("gRPC 方法名 %q 无效，格式应为 /package.Service/Method", fullMethod)
	}
	svcName := trimmed[:slash]
	mtdName := trimmed[slash+1:]

	desc, err := files.FindDescriptorByName(protoreflect.FullName(svcName))
	if err != nil {
		return nil, fmt.Errorf("在 Proto 描述集中未找到服务 %q：%w", svcName, err)
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q 不是有效的 gRPC service 描述", svcName)
	}
	md := sd.Methods().ByName(protoreflect.Name(mtdName))
	if md == nil {
		return nil, fmt.Errorf("服务 %q 中不存在方法 %q", svcName, mtdName)
	}
	return &MethodCodec{
		FullMethod: "/" + svcName + "/" + mtdName,
		InDesc:     md.Input(),
		OutDesc:    md.Output(),
	}, nil
}

// EncodeJSON marshals a JSON request payload into protobuf wire format.
func (c *MethodCodec) EncodeJSON(raw []byte) ([]byte, error) {
	msg := dynamicpb.NewMessage(c.InDesc)
	if len(bytes.TrimSpace(raw)) == 0 {
		return proto.Marshal(msg)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("JSON 转 Protobuf 失败（%s）：%w", c.InDesc.FullName(), err)
	}
	return proto.Marshal(msg)
}

// DecodeToJSON unmarshals a protobuf wire response into JSON bytes.
func (c *MethodCodec) DecodeToJSON(wire []byte) ([]byte, error) {
	msg := dynamicpb.NewMessage(c.OutDesc)
	if len(wire) > 0 {
		if err := proto.Unmarshal(wire, msg); err != nil {
			return nil, fmt.Errorf("Protobuf 响应解码失败（%s）：%w", c.OutDesc.FullName(), err)
		}
	}
	return (protojson.MarshalOptions{
		EmitUnpopulated: true,
		UseProtoNames:   true,
	}).Marshal(msg)
}

// SampleJSON produces a human-readable example JSON payload for md with a depth guard.
func SampleJSON(md protoreflect.MessageDescriptor) string {
	if md == nil {
		return "{}"
	}
	msg := populateSampleMessage(md, 0)
	b, err := (protojson.MarshalOptions{
		EmitUnpopulated: true,
		UseProtoNames:   true,
		Indent:          "  ",
	}).Marshal(msg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func populateSampleMessage(md protoreflect.MessageDescriptor, depth int) *dynamicpb.Message {
	msg := dynamicpb.NewMessage(md)
	if depth >= 3 {
		return msg
	}
	seenOneof := make(map[protoreflect.OneofDescriptor]bool)
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if oo := fd.ContainingOneof(); oo != nil && !fd.HasOptionalKeyword() {
			if seenOneof[oo] {
				continue
			}
			seenOneof[oo] = true
		}
		if fd.IsMap() {
			m := msg.Mutable(fd).Map()
			k := sampleScalarValue(fd.MapKey()).MapKey()
			valFd := fd.MapValue()
			if valFd.Kind() == protoreflect.MessageKind {
				m.Set(k, protoreflect.ValueOfMessage(populateSampleMessage(valFd.Message(), depth+1)))
			} else {
				m.Set(k, sampleScalarValue(valFd))
			}
			continue
		}
		if fd.IsList() {
			l := msg.Mutable(fd).List()
			if fd.Kind() == protoreflect.MessageKind {
				l.Append(protoreflect.ValueOfMessage(populateSampleMessage(fd.Message(), depth+1)))
			} else {
				l.Append(sampleScalarValue(fd))
			}
			continue
		}
		if fd.Kind() == protoreflect.MessageKind {
			msg.Set(fd, protoreflect.ValueOfMessage(populateSampleMessage(fd.Message(), depth+1)))
			continue
		}
		msg.Set(fd, sampleScalarValue(fd))
	}
	return msg
}

func sampleScalarValue(fd protoreflect.FieldDescriptor) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(false)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(0)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(0)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(0)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(0)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(0)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(0)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("")
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes(nil)
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		if vals.Len() > 0 {
			return protoreflect.ValueOfEnum(vals.Get(0).Number())
		}
		return protoreflect.ValueOfEnum(0)
	default:
		return protoreflect.Value{}
	}
}
