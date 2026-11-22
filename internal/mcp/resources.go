package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tidegate/internal/auth"
)

func (s *Server) listMCPResources(ctx context.Context, u *auth.User) ([]Resource, *rpcError) {
	if err := auth.CheckScope(ctx, "read"); err != nil {
		return nil, &rpcError{Code: CodeInvalidRequest, Message: err.Error()}
	}
	projects, err := s.accessibleProjects(ctx, u)
	if err != nil {
		return nil, &rpcError{Code: CodeInternalError, Message: err.Error()}
	}
	out := []Resource{
		{
			URI:         "tidegate://projects",
			Name:        "Tidegate 项目列表（已脱敏）",
			Description: "当前账号/令牌有权访问的全部项目元数据及脱敏环境变量列表。",
			MimeType:    "application/json",
		},
	}
	for _, pr := range projects {
		out = append(out, Resource{
			URI:         "tidegate://projects/" + pr.ID + "/workspace",
			Name:        "工作区概览：" + pr.Name,
			Description: "包含目录树、已脱敏环境列表、压测场景摘要及测试数据文件列表。",
			MimeType:    "application/json",
		})
	}
	return out, nil
}

func (s *Server) listMCPResourceTemplates() []ResourceTemplate {
	return []ResourceTemplate{
		{
			URITemplate: "tidegate://projects/{project_id}/workspace",
			Name:        "项目工作区全景",
			Description: "获取指定项目的目录接口树（敏感字段已脱敏）、环境、场景及数据文件。",
			MimeType:    "application/json",
		},
		{
			URITemplate: "tidegate://projects/{project_id}/environments",
			Name:        "项目环境列表",
			Description: "读取指定项目所有环境配置、BaseURL 与脱敏变量表。",
			MimeType:    "application/json",
		},
		{
			URITemplate: "tidegate://runs/{run_id}/summary",
			Name:        "压测任务诊断摘要",
			Description: "读取单次压测任务的关键指标 KPI、SLA 阈值评估及告警记录。",
			MimeType:    "application/json",
		},
	}
}

func (s *Server) readMCPResource(ctx context.Context, u *auth.User, rawParams json.RawMessage) (*ReadResourceResult, *rpcError) {
	if err := auth.CheckScope(ctx, "read"); err != nil {
		return nil, &rpcError{Code: CodeInvalidRequest, Message: err.Error()}
	}
	in, err := decodeArgs[ReadResourceParams](rawParams)
	if err != nil || strings.TrimSpace(in.URI) == "" {
		return nil, &rpcError{Code: CodeInvalidParams, Message: "resources/read 参数无效，缺少 uri"}
	}
	uri := strings.TrimSpace(in.URI)
	if !strings.HasPrefix(uri, "tidegate://") {
		return nil, &rpcError{Code: CodeInvalidParams, Message: fmt.Sprintf("不支持的 Resource URI：%q", uri)}
	}
	rest := strings.TrimPrefix(uri, "tidegate://")
	parts := strings.Split(strings.Trim(rest, "/"), "/")

	var payload any
	switch {
	case len(parts) == 1 && parts[0] == "projects":
		list, err := s.accessibleProjects(ctx, u)
		if err != nil {
			return nil, &rpcError{Code: CodeInternalError, Message: err.Error()}
		}
		payload = map[string]any{"projects": list, "count": len(list)}

	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "workspace":
		pid := parts[1]
		if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
			return nil, mapResourceAuthErr(err)
		}
		ws, err := s.buildProjectWorkspace(pid)
		if err != nil {
			return nil, mapResourceAuthErr(err)
		}
		payload = ws

	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "environments":
		pid := parts[1]
		if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
			return nil, mapResourceAuthErr(err)
		}
		envs, err := s.store.ListEnvironments(pid)
		if err != nil {
			return nil, mapResourceAuthErr(err)
		}
		payload = map[string]any{"project_id": pid, "environments": envs}

	case len(parts) == 3 && parts[0] == "runs" && parts[2] == "summary":
		rid := parts[1]
		rep, err := s.buildRunReport(ctx, u, rid, 0)
		if err != nil {
			return nil, mapResourceAuthErr(err)
		}
		payload = rep

	default:
		return nil, &rpcError{Code: CodeInvalidParams, Message: fmt.Sprintf("未找到匹配的 MCP 资源 URI：%q", uri)}
	}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, &rpcError{Code: CodeInternalError, Message: fmt.Sprintf("序列化资源内容失败：%v", err)}
	}
	text := truncateUTF8Bytes(string(b), maxMCPOutputBytes)
	return &ReadResourceResult{
		Contents: []ResourceContents{{
			URI:      uri,
			MimeType: "application/json",
			Text:     text,
		}},
	}, nil
}

func mapResourceAuthErr(err error) *rpcError {
	if errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrScopeForbidden) {
		return &rpcError{Code: CodeInvalidRequest, Message: err.Error()}
	}
	return &rpcError{Code: CodeInvalidParams, Message: err.Error()}
}
