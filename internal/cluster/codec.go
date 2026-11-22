package cluster

import (
	"fmt"

	"google.golang.org/grpc"
)

const (
	serviceName   = "tidegate.cluster.v1.Cluster"
	connectMethod = "/" + serviceName + "/Connect"
)

// frame is a message that has already been encoded. Senders marshal when a
// message is queued, so size errors surface to the caller and the sending
// goroutine only writes bytes.
type frame []byte

// codec encodes the hand-written message types. It is installed explicitly
// on both ends (grpc.ForceServerCodec / grpc.ForceCodec) instead of being
// registered globally, so the process-wide "proto" codec stays untouched.
// It reports the name "proto" so the content type on the wire is the same
// as with generated code.
type codec struct{}

func (codec) Name() string { return "proto" }

func (codec) Marshal(v any) ([]byte, error) {
	switch m := v.(type) {
	case frame:
		return m, nil
	case *NodeMessage:
		return m.Marshal()
	case *ControllerMessage:
		return m.Marshal()
	}
	return nil, fmt.Errorf("cluster: cannot marshal %T", v)
}

// Unmarshal decodes into *NodeMessage or *ControllerMessage. gRPC hands a
// freshly materialized buffer to v1 codecs, so the decoded []byte fields may
// alias it safely.
func (codec) Unmarshal(data []byte, v any) error {
	switch m := v.(type) {
	case *NodeMessage:
		return m.Unmarshal(data)
	case *ControllerMessage:
		return m.Unmarshal(data)
	}
	return fmt.Errorf("cluster: cannot unmarshal into %T", v)
}

// connectHandler is the service interface registered with grpc.Server.
type connectHandler interface {
	connect(stream grpc.ServerStream) error
}

var connectStreamDesc = grpc.StreamDesc{
	StreamName:    "Connect",
	ServerStreams: true,
	ClientStreams: true,
}

var serviceDesc = grpc.ServiceDesc{
	ServiceName: serviceName,
	HandlerType: (*connectHandler)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName: "Connect",
		Handler: func(srv any, stream grpc.ServerStream) error {
			return srv.(connectHandler).connect(stream)
		},
		ServerStreams: true,
		ClientStreams: true,
	}},
	Metadata: "internal/cluster/cluster.proto",
}

func encodeNode(b NodeBody, limit int) (frame, error) {
	data, err := (&NodeMessage{Body: b}).Marshal()
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, tooLarge(len(data), limit)
	}
	return frame(data), nil
}

func encodeController(b ControllerBody, limit int) (frame, error) {
	data, err := (&ControllerMessage{Body: b}).Marshal()
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, tooLarge(len(data), limit)
	}
	return frame(data), nil
}
