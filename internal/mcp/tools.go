package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tidegate/internal/auth"
	"tidegate/internal/controller"
	"tidegate/internal/curl"
	"tidegate/internal/importer"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type toolDef struct {
	spec     Tool
	readOnly bool
	scope    string
	longOp   bool
	run      func(s *Server, ctx context.Context, u *auth.User, pol controller.SecurityPolicy, raw json.RawMessage) (any, error)
}

func decodeArgs[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("参数格式错误：%w", err)
	}
	return v, nil
}

func buildTools() []toolDef {
	ro := func(title string) *ToolAnnotations {
		return &ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(false),
		}
	}
	wr := func(title string, idempotent, openWorld bool) *ToolAnnotations {
		return &ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    boolPtr(false),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(idempotent),
			OpenWorldHint:   boolPtr(openWorld),
		}
	}

	return []toolDef{
		// 1. list_projects
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "list_projects",
				Description: "列出当前 Tidegate 实例中的所有项目及其 ID、名称、描述和已掩码变量列表（密钥自动打码）。",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
				Annotations: ro("列出项目"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, _ json.RawMessage) (any, error) {
				list, err := s.accessibleProjects(ctx, u)
				if err != nil {
					return nil, err
				}
				return map[string]any{"projects": list, "count": len(list)}, nil
			},
		},

		// 2. get_project_workspace
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "get_project_workspace",
				Description: "获取指定项目的完整工作区概览：目录接口树（敏感凭据已脱敏）、环境与 BaseURL、压测场景列表与测试数据文件。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id": map[string]any{"type": "string", "description": "目标项目 ID"},
					},
					"required": []string{"project_id"},
				},
				Annotations: ro("获取项目工作区全景"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID string `json:"project_id"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供有效的 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
					return nil, err
				}
				return s.buildProjectWorkspace(pid)
			},
		},

		// 3. get_api_detail
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "get_api_detail",
				Description: "读取单个接口完整定义、生效目录继承及环境变量预览（强制脱敏密钥）与脱敏 cURL 命令。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":     map[string]any{"type": "string", "description": "项目 ID"},
						"api_id":         map[string]any{"type": "string", "description": "接口 ID"},
						"environment_id": map[string]any{"type": "string", "description": "可选：用于计算生效 URL 与变量的环境 ID"},
					},
					"required": []string{"project_id", "api_id"},
				},
				Annotations: ro("读取接口详情与脱敏预览"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID     string `json:"project_id"`
					APIID         string `json:"api_id"`
					EnvironmentID string `json:"environment_id"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.APIID) == "" {
					return nil, errors.New("必须提供 project_id 和 api_id")
				}
				pid, aid := strings.TrimSpace(in.ProjectID), strings.TrimSpace(in.APIID)
				if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
					return nil, err
				}
				a, err := s.store.GetAPI(pid, aid)
				if err != nil {
					return nil, err
				}
				preview, err := s.store.PreviewEffectiveAPI(pid, a, store.ResolveOptions{
					EnvironmentID:  strings.TrimSpace(in.EnvironmentID),
					IncludeSecrets: false,
				})
				if err != nil {
					return nil, err
				}
				curlCmd, _ := curl.FormatAPI(a, preview, "expanded", "bash", true)
				return map[string]any{
					"api":               redactAPISecrets(a),
					"effective_preview": redactEffectivePreview(preview),
					"curl_redacted":     curlCmd,
				}, nil
			},
		},

		// 4. get_scenario_detail
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "get_scenario_detail",
				Description: "读取多步骤压测场景的编排步骤（含控制流）、数据绑定、负载阶段配置与 SLA 阈值定义。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":  map[string]any{"type": "string"},
						"scenario_id": map[string]any{"type": "string"},
					},
					"required": []string{"project_id", "scenario_id"},
				},
				Annotations: ro("读取压测场景详情"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID  string `json:"project_id"`
					ScenarioID string `json:"scenario_id"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.ScenarioID) == "" {
					return nil, errors.New("必须提供 project_id 和 scenario_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
					return nil, err
				}
				sc, err := s.store.GetScenario(pid, strings.TrimSpace(in.ScenarioID))
				if err != nil {
					return nil, err
				}
				return redactScenarioSecrets(sc), nil
			},
		},

		// 5. estimate_load_run
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "estimate_load_run",
				Description: "压测启动前干算预估峰值 RPS、预估上行带宽与在线发压节点容量充足度及建议。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":     map[string]any{"type": "string"},
						"scenario_id":    map[string]any{"type": "string", "description": "场景 ID（与 api_id 二选一）"},
						"api_id":         map[string]any{"type": "string", "description": "单接口 ID（与 scenario_id 二选一）"},
						"environment_id": map[string]any{"type": "string"},
						"load":           map[string]any{"type": "object", "description": "可选：临时覆盖负载阶段配置"},
						"node_selector":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
					},
					"required": []string{"project_id"},
				},
				Annotations: ro("预估压测容量与带宽"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID     string            `json:"project_id"`
					ScenarioID    string            `json:"scenario_id"`
					APIID         string            `json:"api_id"`
					EnvironmentID string            `json:"environment_id"`
					Load          *scenario.Load    `json:"load"`
					NodeSelector  map[string]string `json:"node_selector"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供有效的 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
					return nil, err
				}
				return s.ctrl.EstimateRun(ctx, pid, controller.StartRunRequest{
					ScenarioID:    strings.TrimSpace(in.ScenarioID),
					APIID:         strings.TrimSpace(in.APIID),
					EnvironmentID: strings.TrimSpace(in.EnvironmentID),
					Load:          in.Load,
					NodeSelector:  in.NodeSelector,
				})
			},
		},

		// 6. list_runs
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "list_runs",
				Description: "分页查询项目的历史与当前压测任务摘要列表（含状态、起止时间与关键吞吐/延迟摘要）。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id": map[string]any{"type": "string"},
						"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
						"offset":     map[string]any{"type": "integer", "minimum": 0},
					},
					"required": []string{"project_id"},
				},
				Annotations: ro("查询压测历史列表"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID string `json:"project_id"`
					Limit     int    `json:"limit"`
					Offset    int    `json:"offset"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
					return nil, err
				}
				if _, err := s.store.GetProject(pid); err != nil {
					return nil, err
				}
				limit := in.Limit
				if limit <= 0 {
					limit = 20
				}
				if limit > 50 {
					limit = 50
				}
				runs, err := s.results.ListRuns(ctx, pid, store.RunQuery{Limit: limit, Offset: max(0, in.Offset)})
				if err != nil {
					return nil, err
				}
				return map[string]any{"runs": runs, "count": len(runs)}, nil
			},
		},

		// 7. get_run_report
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "get_run_report",
				Description: "获取单次压测完整诊断报告：汇总 KPI、SLA 阈值评估结果、实时告警、12 段延迟直方图以及脱敏后的错误/慢请求采样。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"run_id":       map[string]any{"type": "string"},
						"sample_limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
					},
					"required": []string{"run_id"},
				},
				Annotations: ro("获取压测详细诊断报告"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					RunID       string `json:"run_id"`
					SampleLimit *int   `json:"sample_limit"`
				}](raw)
				if err != nil || strings.TrimSpace(in.RunID) == "" {
					return nil, errors.New("必须提供 run_id")
				}
				id := strings.TrimSpace(in.RunID)
				sampleLimit := 5
				if in.SampleLimit != nil {
					sampleLimit = clampInt(*in.SampleLimit, 0, 20)
				}
				return s.buildRunReport(ctx, u, id, sampleLimit)
			},
		},

		// 8. compare_runs
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "compare_runs",
				Description: "对比 2 至 6 次压测任务与基线（第一个 ID）的吞吐、P50/P95/P99/Max、错误率及分步骤变化率。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"run_ids": map[string]any{
							"type":     "array",
							"items":    map[string]any{"type": "string"},
							"minItems": 2,
							"maxItems": 6,
						},
					},
					"required": []string{"run_ids"},
				},
				Annotations: ro("基线性能回归对比"),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					RunIDs []string `json:"run_ids"`
				}](raw)
				if err != nil {
					return nil, err
				}
				for _, rid := range in.RunIDs {
					if r := strings.TrimSpace(rid); r != "" {
						if _, _, err := s.authorizeRun(ctx, u, r, false); err != nil {
							return nil, err
						}
					}
				}
				return s.ctrl.CompareRuns(ctx, in.RunIDs, 0)
			},
		},

		// 9. get_cluster_status
		{
			readOnly: true,
			scope:    "read",
			spec: Tool{
				Name:        "get_cluster_status",
				Description: "查看在线 mTLS 发压节点（CPU/内存/网卡/权重/标签）、当前运行中任务 ID 以及生效的安全与 MCP 压测护栏配额。",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
				Annotations: ro("查看集群与安全策略状态"),
			},
			run: func(s *Server, ctx context.Context, _ *auth.User, pol controller.SecurityPolicy, _ json.RawMessage) (any, error) {
				agents, err := s.ctrl.Agents(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"active_run_id": s.ctrl.ActiveRunID(),
					"online_agents": len(agents),
					"agents":        agents,
					"security_policy": map[string]any{
						"allowlist_enabled":              pol.AllowlistEnabled,
						"allowed_targets_count":          len(pol.AllowedTargets),
						"member_max_vus":                 pol.MemberMaxVUs,
						"member_max_rps":                 pol.MemberMaxRPS,
						"scripts_disabled":               pol.ScriptsDisabled,
						"mcp_disabled":                   pol.MCPDisabled,
						"mcp_allow_load_runs":            !pol.MCPDisableLoadRuns,
						"mcp_disabled_tools":             pol.MCPDisabledTools,
						"effective_mcp_max_vus":          pol.EffectiveMCPMaxVUs(),
						"effective_mcp_max_duration_sec": int(pol.EffectiveMCPMaxDuration().Seconds()),
					},
				}, nil
			},
		},

		// 10. save_api
		{
			readOnly: false,
			scope:    "write",
			spec: Tool{
				Name:        "save_api",
				Description: "在项目中创建或更新接口定义（支持 HTTP/WS/gRPC/SSE、请求参数、Headers、Body、认证、提取器、断言及文档）。未传 version 更新时自动使用最新版本锁。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id": map[string]any{"type": "string"},
						"api_id":     map[string]any{"type": "string", "description": "为空时创建新接口；传入时更新对应接口"},
						"api":        map[string]any{"type": "object", "description": "store.API 结构（包含 name、folder_id、request、auth、extract、assert 等）"},
					},
					"required": []string{"project_id", "api"},
				},
				Annotations: wr("保存或更新接口定义", true, false),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID string     `json:"project_id"`
					APIID     string     `json:"api_id"`
					API       *store.API `json:"api"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" || in.API == nil {
					return nil, errors.New("必须提供 project_id 和 api 对象")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
					return nil, err
				}
				aid := strings.TrimSpace(in.APIID)
				if aid == "" {
					aid = strings.TrimSpace(in.API.ID)
				}
				if aid == "" {
					created, err := s.store.CreateAPI(pid, in.API)
					if err != nil {
						return nil, err
					}
					return redactAPISecrets(created), nil
				}
				in.API.ID = aid
				if strings.TrimSpace(in.API.Version) == "" {
					existing, err := s.store.GetAPI(pid, aid)
					if err != nil {
						return nil, err
					}
					in.API.Version = existing.Version
				}
				updated, err := s.store.UpdateAPI(pid, in.API)
				if err != nil {
					return nil, err
				}
				return redactAPISecrets(updated), nil
			},
		},

		// 11. import_api_spec
		{
			readOnly: false,
			scope:    "read",
			spec: Tool{
				Name:        "import_api_spec",
				Description: "预览或导入 OpenAPI 3 / Swagger 2 / Postman / Apifox / HAR / JMeter / cURL 规范到项目中。默认 mode=preview（仅差异预览），需确认后再传 mode=apply 写入。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":       map[string]any{"type": "string"},
						"mode":             map[string]any{"type": "string", "enum": []string{"preview", "apply"}},
						"format":           map[string]any{"type": "string", "description": "auto / openapi3 / swagger2 / postman / har / curl / jmeter / apifox"},
						"content":          map[string]any{"type": "string", "description": "待解析的规范原文或 cURL 命令"},
						"filename":         map[string]any{"type": "string"},
						"strategy":         map[string]any{"type": "string", "enum": []string{"merge", "overwrite", "skip"}},
						"target_folder_id": map[string]any{"type": "string"},
					},
					"required": []string{"project_id", "content"},
				},
				Annotations: wr("预览或导入接口集合", false, false),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID      string `json:"project_id"`
					Mode           string `json:"mode"`
					Format         string `json:"format"`
					Content        string `json:"content"`
					Filename       string `json:"filename"`
					Strategy       string `json:"strategy"`
					TargetFolderID string `json:"target_folder_id"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.Content) == "" {
					return nil, errors.New("必须提供 project_id 和 content")
				}
				pid := strings.TrimSpace(in.ProjectID)
				imp := importer.PreviewInput{
					Format:         strings.TrimSpace(in.Format),
					Filename:       strings.TrimSpace(in.Filename),
					Content:        []byte(in.Content),
					Strategy:       strings.TrimSpace(in.Strategy),
					TargetFolderID: strings.TrimSpace(in.TargetFolderID),
				}
				mode := strings.ToLower(strings.TrimSpace(in.Mode))
				if mode == "" || mode == "preview" {
					if _, err := s.authorizeProject(ctx, u, pid, false); err != nil {
						return nil, err
					}
					return importer.Preview(s.store, pid, imp)
				}
				if mode == "apply" {
					if err := auth.CheckScope(ctx, "write"); err != nil {
						return nil, err
					}
					if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
						return nil, err
					}
					return importer.Apply(s.store, pid, imp)
				}
				return nil, fmt.Errorf("不支持的导入模式 %q，可选 preview 或 apply", in.Mode)
			},
		},

		// 12. save_scenario
		{
			readOnly: false,
			scope:    "write",
			spec: Tool{
				Name:        "save_scenario",
				Description: "创建或更新压测场景（包含有序步骤、group/loop/if/weighted 控制流、script 步骤、CSV/JSONL 数据源、负载阶段与 SLA 阈值）。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":  map[string]any{"type": "string"},
						"scenario_id": map[string]any{"type": "string", "description": "为空时创建新场景；传入时更新"},
						"scenario":    map[string]any{"type": "object"},
					},
					"required": []string{"project_id", "scenario"},
				},
				Annotations: wr("保存或更新压测场景", true, false),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, pol controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID  string          `json:"project_id"`
					ScenarioID string          `json:"scenario_id"`
					Scenario   *store.Scenario `json:"scenario"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" || in.Scenario == nil {
					return nil, errors.New("必须提供 project_id 和 scenario 对象")
				}
				if pol.ScriptsDisabled && storeStepsHaveScript(in.Scenario.Steps) {
					return nil, controller.ErrScriptsDisabled
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
					return nil, err
				}
				sid := strings.TrimSpace(in.ScenarioID)
				if sid == "" {
					sid = strings.TrimSpace(in.Scenario.ID)
				}
				if sid == "" {
					created, err := s.store.CreateScenario(pid, in.Scenario)
					if err != nil {
						return nil, err
					}
					return redactScenarioSecrets(created), nil
				}
				in.Scenario.ID = sid
				if strings.TrimSpace(in.Scenario.Version) == "" {
					existing, err := s.store.GetScenario(pid, sid)
					if err != nil {
						return nil, err
					}
					in.Scenario.Version = existing.Version
				}
				updated, err := s.store.UpdateScenario(pid, in.Scenario)
				if err != nil {
					return nil, err
				}
				return redactScenarioSecrets(updated), nil
			},
		},

		// 13. debug_api
		{
			readOnly: false,
			scope:    "debug",
			longOp:   true,
			spec: Tool{
				Name:        "debug_api",
				Description: "发送单次真实接口调试请求，返回 5 阶段耗时瀑布、提取变量、断言通过率与脱敏响应（严格遵循白名单防 SSRF，密文与敏感请求头自动脱敏，单次响应截取 <=32 KiB）。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":     map[string]any{"type": "string"},
						"api_id":         map[string]any{"type": "string", "description": "已保存接口 ID（与 api 草稿二选一）"},
						"api":            map[string]any{"type": "object", "description": "未保存接口定义草稿"},
						"environment_id": map[string]any{"type": "string"},
						"variables":      map[string]any{"type": "object", "additionalProperties": true},
					},
					"required": []string{"project_id"},
				},
				Annotations: wr("安全执行单接口调试", false, true),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID     string                     `json:"project_id"`
					APIID         string                     `json:"api_id"`
					API           *store.API                 `json:"api"`
					EnvironmentID string                     `json:"environment_id"`
					Variables     map[string]scenario.Scalar `json:"variables"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
					return nil, err
				}
				return s.ctrl.DebugAPI(ctx, pid, controller.DebugAPIRequest{
					APIID:         strings.TrimSpace(in.APIID),
					API:           in.API,
					EnvironmentID: strings.TrimSpace(in.EnvironmentID),
					Variables:     in.Variables,
					MaxBodyBytes:  32 << 10,
					RedactSecrets: true,
				})
			},
		},

		// 14. debug_scenario
		{
			readOnly: false,
			scope:    "debug",
			longOp:   true,
			spec: Tool{
				Name:        "debug_scenario",
				Description: "以单虚拟用户运行一次多步骤场景试跑（Dry-Run），逐步骤校验变量传递、控制流分支与断言结果（严格校验白名单与脚本策略，响应全量脱敏）。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":     map[string]any{"type": "string"},
						"scenario_id":    map[string]any{"type": "string"},
						"scenario":       map[string]any{"type": "object"},
						"environment_id": map[string]any{"type": "string"},
						"variables":      map[string]any{"type": "object", "additionalProperties": true},
						"stop_on_error":  map[string]any{"type": "boolean"},
					},
					"required": []string{"project_id"},
				},
				Annotations: wr("安全执行场景单次试跑", false, true),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID     string                     `json:"project_id"`
					ScenarioID    string                     `json:"scenario_id"`
					Scenario      *store.Scenario            `json:"scenario"`
					EnvironmentID string                     `json:"environment_id"`
					Variables     map[string]scenario.Scalar `json:"variables"`
					StopOnError   *bool                      `json:"stop_on_error"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
					return nil, err
				}
				stopOnErr := true
				if in.StopOnError != nil {
					stopOnErr = *in.StopOnError
				}
				return s.ctrl.DebugScenario(ctx, pid, controller.DebugScenarioRequest{
					ScenarioID:    strings.TrimSpace(in.ScenarioID),
					Scenario:      in.Scenario,
					EnvironmentID: strings.TrimSpace(in.EnvironmentID),
					Variables:     in.Variables,
					StopOnError:   stopOnErr,
					MaxBodyBytes:  16 << 10,
					RedactSecrets: true,
				})
			},
		},

		// 15. manage_load_run
		{
			readOnly: false,
			scope:    "run",
			spec: Tool{
				Name:        "manage_load_run",
				Description: "受控管理分布式压测生命周期（start / set_target / graceful_stop / abort）。启动压测严格受管理员配置的 MCP 并发 VUs 上限、最大时长上限、角色配额及目标白名单保护。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":              map[string]any{"type": "string", "enum": []string{"start", "set_target", "graceful_stop", "abort"}},
						"project_id":          map[string]any{"type": "string", "description": "action=start 时必填"},
						"scenario_id":         map[string]any{"type": "string"},
						"api_id":              map[string]any{"type": "string"},
						"environment_id":      map[string]any{"type": "string"},
						"load":                map[string]any{"type": "object"},
						"node_selector":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
						"node_failure_policy": map[string]any{"type": "string", "enum": []string{"redistribute", "abort"}},
						"run_id":              map[string]any{"type": "string", "description": "action 为 set_target / graceful_stop / abort 时必填"},
						"target":              map[string]any{"type": "number", "description": "action=set_target 时的目标 VU 或 RPS"},
					},
					"required": []string{"action"},
				},
				Annotations: &ToolAnnotations{
					Title:           "受控压测生命周期管理",
					ReadOnlyHint:    boolPtr(false),
					DestructiveHint: boolPtr(true),
					IdempotentHint:  boolPtr(false),
					OpenWorldHint:   boolPtr(true),
				},
			},
			run: func(s *Server, ctx context.Context, u *auth.User, pol controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					Action            string                     `json:"action"`
					ProjectID         string                     `json:"project_id"`
					ScenarioID        string                     `json:"scenario_id"`
					APIID             string                     `json:"api_id"`
					EnvironmentID     string                     `json:"environment_id"`
					Variables         map[string]scenario.Scalar `json:"variables"`
					Load              *scenario.Load             `json:"load"`
					NodeSelector      map[string]string          `json:"node_selector"`
					NodeFailurePolicy string                     `json:"node_failure_policy"`
					RunID             string                     `json:"run_id"`
					Target            float64                    `json:"target"`
				}](raw)
				if err != nil {
					return nil, err
				}
				if pol.MCPDisableLoadRuns {
					return nil, errors.New("管理员已在安全策略中禁用通过 MCP 控制或启动压测任务")
				}
				switch strings.ToLower(strings.TrimSpace(in.Action)) {
				case "start":
					pid := strings.TrimSpace(in.ProjectID)
					if pid == "" {
						return nil, errors.New("action=start 时必须提供 project_id")
					}
					effRole, err := s.authorizeProject(ctx, u, pid, true)
					if err != nil {
						return nil, err
					}
					return s.ctrl.StartRun(ctx, pid, controller.StartRunRequest{
						ScenarioID:        strings.TrimSpace(in.ScenarioID),
						APIID:             strings.TrimSpace(in.APIID),
						EnvironmentID:     strings.TrimSpace(in.EnvironmentID),
						Variables:         in.Variables,
						Load:              in.Load,
						NodeSelector:      in.NodeSelector,
						NodeFailurePolicy: strings.TrimSpace(in.NodeFailurePolicy),
						ActorRole:         string(effRole),
						MCPSource:         true,
					})
				case "set_target":
					rid := strings.TrimSpace(in.RunID)
					if rid == "" {
						return nil, errors.New("action=set_target 时必须提供 run_id")
					}
					run, effRole, rerr := s.authorizeRun(ctx, u, rid, true)
					if rerr != nil {
						return nil, rerr
					}
					if def, derr := run.Definition(); derr == nil && def.Load.Mode != "rate" {
						if in.Target > float64(pol.EffectiveMCPMaxVUs()) {
							return nil, fmt.Errorf("MCP 调整的目标并发 %.0f 超出安全上限 %d VUs", in.Target, pol.EffectiveMCPMaxVUs())
						}
					}
					if err := s.ctrl.SetRunTargetWithRole(ctx, rid, in.Target, string(effRole)); err != nil {
						return nil, err
					}
					return map[string]any{"ok": true, "run_id": rid, "target": in.Target}, nil
				case "graceful_stop":
					rid := strings.TrimSpace(in.RunID)
					if rid == "" {
						return nil, errors.New("必须提供 run_id")
					}
					if _, _, rerr := s.authorizeRun(ctx, u, rid, true); rerr != nil {
						return nil, rerr
					}
					return s.ctrl.StopRun(ctx, rid, false)
				case "abort":
					rid := strings.TrimSpace(in.RunID)
					if rid == "" {
						return nil, errors.New("必须提供 run_id")
					}
					if _, _, rerr := s.authorizeRun(ctx, u, rid, true); rerr != nil {
						return nil, rerr
					}
					return s.ctrl.StopRun(ctx, rid, true)
				default:
					return nil, fmt.Errorf("未知 action %q，可选 start / set_target / graceful_stop / abort", in.Action)
				}
			},
		},

		// 16. run_collection_test
		{
			readOnly: false,
			scope:    "debug",
			longOp:   true,
			spec: Tool{
				Name:        "run_collection_test",
				Description: "按目录树顺序批量执行项目或指定目录下的全部接口功能回归测试（支持跨接口变量传递、前置/后置脚本与断言，敏感响应已自动脱敏）。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id":     map[string]any{"type": "string", "description": "项目 ID"},
						"folder_id":      map[string]any{"type": "string", "description": "可选，仅运行指定目录（支持目录 ID、名称或路径）"},
						"api_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "可选，按给定顺序执行指定接口 ID 列表"},
						"environment_id": map[string]any{"type": "string", "description": "可选环境 ID"},
						"variables":      map[string]any{"type": "object", "description": "可选临时覆盖变量"},
						"stop_on_error":  map[string]any{"type": "boolean", "description": "是否在首个失败接口后停止"},
					},
					"required": []string{"project_id"},
				},
				Annotations: wr("批量运行目录或接口回归测试", false, true),
			},
			run: func(s *Server, ctx context.Context, u *auth.User, _ controller.SecurityPolicy, raw json.RawMessage) (any, error) {
				in, err := decodeArgs[struct {
					ProjectID     string                     `json:"project_id"`
					FolderID      string                     `json:"folder_id"`
					APIIDs        []string                   `json:"api_ids"`
					EnvironmentID string                     `json:"environment_id"`
					Variables     map[string]scenario.Scalar `json:"variables"`
					StopOnError   bool                       `json:"stop_on_error"`
				}](raw)
				if err != nil || strings.TrimSpace(in.ProjectID) == "" {
					return nil, errors.New("必须提供有效的 project_id")
				}
				pid := strings.TrimSpace(in.ProjectID)
				if _, err := s.authorizeProject(ctx, u, pid, true); err != nil {
					return nil, err
				}
				return s.ctrl.RunCollectionTest(ctx, pid, controller.CollectionTestRequest{
					FolderID:      strings.TrimSpace(in.FolderID),
					APIIDs:        in.APIIDs,
					EnvironmentID: strings.TrimSpace(in.EnvironmentID),
					Variables:     in.Variables,
					StopOnError:   in.StopOnError,
				})
			},
		},
	}
}

func storeStepsHaveScript(steps []store.ScenarioStep) bool {
	for i := range steps {
		st := &steps[i]
		if st.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(st.Type), "script") {
			return true
		}
		if storeStepsHaveScript(st.Children) || storeStepsHaveScript(st.Else) {
			return true
		}
		for b := range st.Branches {
			if storeStepsHaveScript(st.Branches[b].Steps) {
				return true
			}
		}
	}
	return false
}

func maskSecretField(v string) string {
	t := strings.TrimSpace(v)
	if t == "" || strings.Contains(t, "{{") {
		return v
	}
	return store.SecretMask
}

func redactAuth(a *store.Auth) *store.Auth {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Token = maskSecretField(cp.Token)
	cp.Password = maskSecretField(cp.Password)
	cp.Value = maskSecretField(cp.Value)
	cp.ClientSecret = maskSecretField(cp.ClientSecret)
	return &cp
}

func redactKeyValues(list store.KeyValueList, isHeaders bool) store.KeyValueList {
	if len(list) == 0 {
		return list
	}
	out := make(store.KeyValueList, len(list))
	for i, kv := range list {
		v := string(kv.Value)
		if kv.Secret && v != "" {
			kv.Value = store.SecretMask
		} else if isHeaders && !strings.Contains(v, "{{") && metrics.RedactHeader(kv.Name, v) != v {
			kv.Value = store.SecretMask
		}
		out[i] = kv
	}
	return out
}

func redactTreeSecrets(t *store.Tree) *store.Tree {
	if t == nil {
		return nil
	}
	cp := *t
	cp.Children = redactTreeNodes(t.Children)
	return &cp
}

func redactTreeNodes(nodes []*store.TreeNode) []*store.TreeNode {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]*store.TreeNode, len(nodes))
	for i, n := range nodes {
		if n == nil {
			continue
		}
		cp := *n
		cp.Headers = redactKeyValues(cp.Headers, true)
		cp.Auth = redactAuth(cp.Auth)
		cp.Children = redactTreeNodes(cp.Children)
		out[i] = &cp
	}
	return out
}

func redactAPISecrets(a *store.API) *store.API {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Request.Headers = redactKeyValues(cp.Request.Headers, true)
	cp.Auth = redactAuth(cp.Auth)
	return &cp
}

func redactEffectivePreview(p *store.EffectivePreview) *store.EffectivePreview {
	if p == nil {
		return nil
	}
	cp := *p
	cp.EffectiveAuth.Auth = redactAuth(cp.EffectiveAuth.Auth)
	if len(p.InheritedHeaders) > 0 {
		ih := make([]store.InheritedHeader, len(p.InheritedHeaders))
		for i, h := range p.InheritedHeaders {
			if !strings.Contains(h.Value, "{{") && metrics.RedactHeader(h.Name, h.Value) != h.Value {
				h.Value = store.SecretMask
			}
			ih[i] = h
		}
		cp.InheritedHeaders = ih
	}
	return &cp
}

func redactScenarioSecrets(sc *store.Scenario) *store.Scenario {
	if sc == nil {
		return nil
	}
	cp := *sc
	cp.Variables = redactKeyValues(cp.Variables, false)
	cp.Steps = redactScenarioSteps(cp.Steps)
	return &cp
}

func redactScenarioSteps(steps []store.ScenarioStep) []store.ScenarioStep {
	if len(steps) == 0 {
		return steps
	}
	out := make([]store.ScenarioStep, len(steps))
	for i, st := range steps {
		if st.Override != nil {
			ov := *st.Override
			ov.Headers = redactKeyValues(ov.Headers, true)
			ov.Auth = redactAuth(ov.Auth)
			st.Override = &ov
		}
		st.Children = redactScenarioSteps(st.Children)
		st.Else = redactScenarioSteps(st.Else)
		if len(st.Branches) > 0 {
			brs := make([]store.ScenarioWeightedBranch, len(st.Branches))
			for j, br := range st.Branches {
				br.Steps = redactScenarioSteps(br.Steps)
				brs[j] = br
			}
			st.Branches = brs
		}
		out[i] = st
	}
	return out
}

func redactSampleRecordData(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var sv metrics.SampleView
	if err := json.Unmarshal(raw, &sv); err != nil {
		return raw
	}
	sv.RequestHeaders = redactSampleHeaders(sv.RequestHeaders)
	sv.ResponseHeaders = redactSampleHeaders(sv.ResponseHeaders)
	b, err := json.Marshal(sv)
	if err != nil {
		return raw
	}
	return b
}

func redactSampleHeaders(hdrs []metrics.Header) []metrics.Header {
	if len(hdrs) == 0 {
		return hdrs
	}
	out := make([]metrics.Header, len(hdrs))
	for i, h := range hdrs {
		out[i] = metrics.Header{
			Name:  h.Name,
			Value: metrics.RedactHeader(h.Name, h.Value),
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
