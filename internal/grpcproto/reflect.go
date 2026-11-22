package grpcproto

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ReflectOptions configure gRPC Server Reflection discovery.
type ReflectOptions struct {
	Target             string
	TLS                bool
	InsecureSkipVerify bool
	Metadata           map[string]string
	Timeout            time.Duration
	DialCheck          func(host string, ip net.IP) error
}

// DiscoverViaReflection connects to a gRPC endpoint supporting Server Reflection
// (trying v1 first and falling back to v1alpha), downloads service file descriptors
// transitively, and returns the method catalog and compiled descriptor registry.
func DiscoverViaReflection(ctx context.Context, opts ReflectOptions) (*SchemaCatalog, *protoregistry.Files, error) {
	if strings.TrimSpace(opts.Target) == "" {
		return nil, nil, errors.New("gRPC Reflection 目标地址不能为空")
	}
	target := strings.TrimSpace(opts.Target)
	target = strings.TrimPrefix(target, "grpc://")
	target = strings.TrimPrefix(target, "grpcs://")
	if i := strings.IndexByte(target, '/'); i >= 0 {
		target = target[:i]
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, nil, fmt.Errorf("gRPC 目标地址 %q 格式无效，应为 host:port", opts.Target)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	creds := insecure.NewCredentials()
	if opts.TLS {
		creds = credentials.NewTLS(&tls.Config{
			ServerName:         host,
			InsecureSkipVerify: opts.InsecureSkipVerify,
			NextProtos:         []string{"h2"},
		})
	}
	dialer := &net.Dialer{Timeout: min(opts.Timeout, 5*time.Second)}
	cc, err := grpc.NewClient(
		"passthrough:///"+target,
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(dctx context.Context, addr string) (net.Conn, error) {
			h, port, serr := net.SplitHostPort(addr)
			if serr != nil {
				return nil, serr
			}
			ips, lerr := net.DefaultResolver.LookupIPAddr(dctx, h)
			if lerr != nil || len(ips) == 0 {
				return nil, fmt.Errorf("解析 %q 失败：%v", h, lerr)
			}
			for _, ipAddr := range ips {
				if opts.DialCheck != nil {
					if cerr := opts.DialCheck(h, ipAddr.IP); cerr != nil {
						return nil, cerr
					}
				}
				c, derr := dialer.DialContext(dctx, "tcp", net.JoinHostPort(ipAddr.IP.String(), port))
				if derr == nil {
					return c, nil
				}
				err = derr
			}
			return nil, err
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 gRPC Reflection 服务失败：%w", err)
	}
	defer cc.Close()

	if len(opts.Metadata) > 0 {
		pairs := make([]string, 0, len(opts.Metadata)*2)
		for k, v := range opts.Metadata {
			pairs = append(pairs, strings.ToLower(strings.TrimSpace(k)), v)
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(pairs...))
	}

	rawFiles, err := fetchReflectionV1(ctx, cc)
	if err != nil && status.Code(err) == codes.Unimplemented {
		rawFiles, err = fetchReflectionV1Alpha(ctx, cc)
	}
	if err != nil {
		return nil, nil, err
	}

	fds := &descriptorpb.FileDescriptorSet{File: make([]*descriptorpb.FileDescriptorProto, 0, len(rawFiles))}
	for _, b := range rawFiles {
		var fdp descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal(b, &fdp); err != nil {
			return nil, nil, fmt.Errorf("解码 Reflection FileDescriptorProto 失败：%w", err)
		}
		fds.File = append(fds.File, &fdp)
	}
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, nil, fmt.Errorf("构建 gRPC Proto 描述集失败：%w", err)
	}
	return BuildCatalog(files), files, nil
}

func fetchReflectionV1(ctx context.Context, cc *grpc.ClientConn) (map[string][]byte, error) {
	stream, err := grpc_reflection_v1.NewServerReflectionClient(cc).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.CloseSend()

	if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		return nil, err
	}
	listResp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	if er := listResp.GetErrorResponse(); er != nil {
		return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
	}

	seen := make(map[string][]byte)
	var queue []string
	var loadFile func(b []byte) error
	loadFile = func(b []byte) error {
		var fdp descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal(b, &fdp); err != nil {
			return err
		}
		name := fdp.GetName()
		if name == "" {
			return nil
		}
		if _, ok := seen[name]; !ok {
			seen[name] = b
			for _, dep := range fdp.Dependency {
				if _, ok := seen[dep]; !ok {
					queue = append(queue, dep)
				}
			}
		}
		return nil
	}

	svcs := listResp.GetListServicesResponse().GetService()
	for _, svc := range svcs {
		name := svc.GetName()
		if name == "" || strings.HasPrefix(name, "grpc.reflection.") {
			continue
		}
		if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: name},
		}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if er := resp.GetErrorResponse(); er != nil {
			return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
		}
		for _, fb := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
			if err := loadFile(fb); err != nil {
				return nil, err
			}
		}
	}

	for len(queue) > 0 {
		dep := queue[0]
		queue = queue[1:]
		if _, ok := seen[dep]; ok {
			continue
		}
		if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_FileByFilename{FileByFilename: dep},
		}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if er := resp.GetErrorResponse(); er != nil {
			if wk := wellKnownDescriptor(dep); wk != nil {
				if wb, merr := proto.Marshal(wk); merr == nil {
					_ = loadFile(wb)
					continue
				}
			}
			return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
		}
		for _, fb := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
			if err := loadFile(fb); err != nil {
				return nil, err
			}
		}
	}
	return seen, nil
}

func fetchReflectionV1Alpha(ctx context.Context, cc *grpc.ClientConn) (map[string][]byte, error) {
	stream, err := grpc_reflection_v1alpha.NewServerReflectionClient(cc).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.CloseSend()

	if err := stream.Send(&grpc_reflection_v1alpha.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1alpha.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		return nil, err
	}
	listResp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	if er := listResp.GetErrorResponse(); er != nil {
		return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
	}

	seen := make(map[string][]byte)
	var queue []string
	loadFile := func(b []byte) error {
		var fdp descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal(b, &fdp); err != nil {
			return err
		}
		name := fdp.GetName()
		if name == "" {
			return nil
		}
		if _, ok := seen[name]; !ok {
			seen[name] = b
			for _, dep := range fdp.Dependency {
				if _, ok := seen[dep]; !ok {
					queue = append(queue, dep)
				}
			}
		}
		return nil
	}

	for _, svc := range listResp.GetListServicesResponse().GetService() {
		name := svc.GetName()
		if name == "" || strings.HasPrefix(name, "grpc.reflection.") {
			continue
		}
		if err := stream.Send(&grpc_reflection_v1alpha.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1alpha.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: name},
		}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if er := resp.GetErrorResponse(); er != nil {
			return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
		}
		for _, fb := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
			if err := loadFile(fb); err != nil {
				return nil, err
			}
		}
	}

	for len(queue) > 0 {
		dep := queue[0]
		queue = queue[1:]
		if _, ok := seen[dep]; ok {
			continue
		}
		if err := stream.Send(&grpc_reflection_v1alpha.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1alpha.ServerReflectionRequest_FileByFilename{FileByFilename: dep},
		}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if er := resp.GetErrorResponse(); er != nil {
			if wk := wellKnownDescriptor(dep); wk != nil {
				if wb, merr := proto.Marshal(wk); merr == nil {
					_ = loadFile(wb)
					continue
				}
			}
			return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
		}
		for _, fb := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
			if err := loadFile(fb); err != nil {
				return nil, err
			}
		}
	}
	return seen, nil
}
