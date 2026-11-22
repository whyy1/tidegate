package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tidegate/internal/grpcproto"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// DiscoverGRPCRequest configures gRPC service discovery via a project .proto/.desc file or Server Reflection.
type DiscoverGRPCRequest struct {
	ProjectID          string         `json:"project_id"`
	EnvironmentID      string         `json:"environment_id,omitempty"`
	Target             string         `json:"target,omitempty"`
	TLS                bool           `json:"tls,omitempty"`
	InsecureSkipVerify bool           `json:"insecure_skip_verify,omitempty"`
	ProtoFile          string         `json:"proto_file,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// DiscoverGRPC inspects a .proto/.desc file in the project or connects to a live gRPC Reflection endpoint.
func (c *Controller) DiscoverGRPC(ctx context.Context, req DiscoverGRPCRequest) (*grpcproto.SchemaCatalog, error) {
	if req.ProjectID == "" {
		return nil, userErr(ErrInvalidInput, "project_id 不能为空", nil)
	}
	if strings.TrimSpace(req.ProtoFile) != "" {
		name := strings.TrimSpace(req.ProtoFile)
		if err := store.ValidateFileName(name); err != nil {
			return nil, userErr(ErrInvalidInput, err.Error(), err)
		}
		dir, err := c.store.FilesDir(req.ProjectID)
		if err != nil {
			return nil, err
		}
		files := map[string][]byte{}
		if list, lerr := c.store.ListFiles(req.ProjectID); lerr == nil {
			for _, df := range list {
				low := strings.ToLower(df.Name)
				if df.Name == name || strings.HasSuffix(low, ".proto") || strings.HasSuffix(low, ".pb") || strings.HasSuffix(low, ".desc") {
					if data, rerr := os.ReadFile(filepath.Join(dir, df.Name)); rerr == nil {
						files[df.Name] = data
					}
				}
			}
		}
		if _, ok := files[name]; !ok {
			return nil, userErr(ErrInvalidInput, fmt.Sprintf("未找到 Proto 文件 %q", name), nil)
		}
		reg, err := grpcproto.ParseProtoFiles(name, files)
		if err != nil {
			return nil, userErr(ErrInvalidInput, err.Error(), err)
		}
		return grpcproto.BuildCatalog(reg), nil
	}

	target := strings.TrimSpace(req.Target)
	if req.EnvironmentID != "" {
		if prev, perr := c.store.PreviewEffectiveAPI(req.ProjectID, &store.API{
			Name:    "grpc-discover",
			Request: store.APIRequest{Protocol: "grpc", URL: target},
		}, store.ResolveOptions{EnvironmentID: req.EnvironmentID, IncludeSecrets: true}); perr == nil && prev != nil && prev.URL != "" {
			target = prev.URL
		}
	}
	if target == "" {
		return nil, userErr(ErrInvalidInput, "请填写 gRPC 目标地址（或选择项目 Proto 描述文件）", nil)
	}

	pol, err := c.GetSecurityPolicy(ctx)
	if err != nil {
		return nil, err
	}
	var dialCheck func(string, net.IP) error
	if len(pol.AllowedTargets) > 0 {
		matcher, merr := scenario.CompileTargetMatcher(pol.AllowedTargets)
		if merr != nil {
			return nil, userErr(ErrInvalidInput, fmt.Sprintf("白名单规则无效：%v", merr), merr)
		}
		dialCheck = func(host string, ip net.IP) error {
			if err := matcher(host, ip); err != nil {
				return errors.New(ErrTargetNotAllowed.Error())
			}
			return nil
		}
	}

	cat, _, err := grpcproto.DiscoverViaReflection(ctx, grpcproto.ReflectOptions{
		Target:             target,
		TLS:                req.TLS,
		InsecureSkipVerify: req.InsecureSkipVerify,
		Metadata:           req.Metadata,
		Timeout:            10 * time.Second,
		DialCheck:          dialCheck,
	})
	if err != nil {
		if strings.Contains(err.Error(), ErrTargetNotAllowed.Error()) {
			return nil, userErr(ErrTargetNotAllowed, "gRPC Reflection 目标不在安全策略允许的目标白名单内", err)
		}
		return nil, userErr(ErrInvalidInput, fmt.Sprintf("gRPC Reflection 探测失败：%v", err), err)
	}
	return cat, nil
}
