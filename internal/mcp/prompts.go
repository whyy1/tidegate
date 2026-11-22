package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

type promptSpec struct {
	prompt Prompt
	render func(args map[string]string) (*GetPromptResult, error)
}

func buildPrompts() []promptSpec {
	return []promptSpec{
		{
			prompt: Prompt{
				Name:        "diagnose_api_failure",
				Description: "诊断单个接口调试或断言失败原因，给出可执行的请求参数/脚本与服务端排查建议。",
				Arguments: []PromptArgument{
					{Name: "project_id", Description: "目标项目 ID", Required: true},
					{Name: "api_id", Description: "失败接口 ID", Required: true},
					{Name: "environment_id", Description: "可选环境 ID", Required: false},
				},
			},
			render: func(args map[string]string) (*GetPromptResult, error) {
				pid := strings.TrimSpace(args["project_id"])
				aid := strings.TrimSpace(args["api_id"])
				if pid == "" || aid == "" {
					return nil, fmt.Errorf("diagnose_api_failure 需要 project_id 和 api_id 参数")
				}
				envID := strings.TrimSpace(args["environment_id"])
				text := fmt.Sprintf(
					"请排查项目 %q 中接口 %q 的调试异常：\n"+
						"1. 调用 get_api_detail(project_id=%q, api_id=%q, environment_id=%q) 检查生效 URL、目录继承 Headers/Auth、前置/后置脚本及断言。\n"+
						"2. 调用 debug_api 在白名单允许下运行单次实时调试，分析五段耗时瀑布（DNS/TCP/TLS/TTFB/Download）与失败明细。\n"+
						"3. 给出精确修改建议，如需调整脚本或断言请在获得用户确认后调用 save_api。",
					pid, aid, pid, aid, envID,
				)
				return &GetPromptResult{
					Description: "Tidegate 单接口失败排查工作流",
					Messages:    []PromptMessage{{Role: "user", Content: ContentBlock{Type: "text", Text: text}}},
				}, nil
			},
		},
		{
			prompt: Prompt{
				Name:        "generate_regression_suite",
				Description: "基于项目或目录接口结构生成前后置脚本、断言规则并执行自动化功能回归测试。",
				Arguments: []PromptArgument{
					{Name: "project_id", Description: "目标项目 ID", Required: true},
					{Name: "folder_id", Description: "可选目录 ID 或名称", Required: false},
				},
			},
			render: func(args map[string]string) (*GetPromptResult, error) {
				pid := strings.TrimSpace(args["project_id"])
				if pid == "" {
					return nil, fmt.Errorf("generate_regression_suite 需要 project_id 参数")
				}
				folder := strings.TrimSpace(args["folder_id"])
				text := fmt.Sprintf(
					"请为项目 %q（目录=%q）构建并运行接口功能回归套件：\n"+
						"1. 读取 tidegate://projects/%s/workspace 或调用 get_project_workspace 查看接口编排顺序、已有提取变量与环境定义。\n"+
						"2. 针对认证/链路依赖接口补齐 post_script 变量提取与 assert(...) 检查。\n"+
						"3. 调用 run_collection_test(project_id=%q, folder_id=%q) 顺序跑通全量用例并汇报通过率与失败明细。",
					pid, folder, pid, pid, folder,
				)
				return &GetPromptResult{
					Description: "功能回归套件生成与批量验收工作流",
					Messages:    []PromptMessage{{Role: "user", Content: ContentBlock{Type: "text", Text: text}}},
				}, nil
			},
		},
		{
			prompt: Prompt{
				Name:        "design_load_scenario",
				Description: "结合业务接口依赖、集群容量与 SLA 要求设计多步骤分布式压测场景并执行容量预估。",
				Arguments: []PromptArgument{
					{Name: "project_id", Description: "目标项目 ID", Required: true},
					{Name: "target_rps", Description: "目标峰值吞吐（RPS）", Required: false},
					{Name: "sla_p99_ms", Description: "目标 P99 延迟阈值（毫秒）", Required: false},
				},
			},
			render: func(args map[string]string) (*GetPromptResult, error) {
				pid := strings.TrimSpace(args["project_id"])
				if pid == "" {
					return nil, fmt.Errorf("design_load_scenario 需要 project_id 参数")
				}
				rps := strings.TrimSpace(args["target_rps"])
				p99 := strings.TrimSpace(args["sla_p99_ms"])
				text := fmt.Sprintf(
					"请为项目 %q 设计分布式压测场景（目标吞吐=%s RPS，P99 门槛=%s ms）：\n"+
						"1. 调用 get_project_workspace 与 get_cluster_status 检查在线 mTLS 节点及 MCP 安全护栏配额。\n"+
						"2. 使用 save_scenario 编排阶梯加压/固定速率阶段，合理配置 on_error、max_dials_per_sec 与 SLA thresholds。\n"+
						"3. 先调用 debug_scenario 确认无变量丢失与断言错误，再调用 estimate_load_run 校验集群 CPU/带宽余量。",
					pid, rps, p99,
				)
				return &GetPromptResult{
					Description: "多步骤分布式压测场景设计工作流",
					Messages:    []PromptMessage{{Role: "user", Content: ContentBlock{Type: "text", Text: text}}},
				}, nil
			},
		},
		{
			prompt: Prompt{
				Name:        "analyze_run_bottleneck",
				Description: "深度对比历史与当前压测任务，识别 P99 尾延迟升高、TLS/建连瓶颈与错误采样根因。",
				Arguments: []PromptArgument{
					{Name: "run_id", Description: "目标压测任务 ID", Required: true},
					{Name: "baseline_run_id", Description: "可选基线任务 ID（用于回归对比）", Required: false},
				},
			},
			render: func(args map[string]string) (*GetPromptResult, error) {
				rid := strings.TrimSpace(args["run_id"])
				if rid == "" {
					return nil, fmt.Errorf("analyze_run_bottleneck 需要 run_id 参数")
				}
				base := strings.TrimSpace(args["baseline_run_id"])
				text := fmt.Sprintf(
					"请分析压测任务 %q（基线对比=%q）的性能瓶颈：\n"+
						"1. 调用 get_run_report(run_id=%q, sample_limit=10) 检查直方图尾延迟、分错误码占比、DNS/Connect/TLS/TTFB 分段耗时及脱敏失败采样。\n"+
						"2. 若提供基线 %q，调用 compare_runs 识别吞吐下降与尾延迟退化步骤。\n"+
						"3. 总结瓶颈归因（客户端端口/建连突发、下游慢响应或 TLS 握手耗时）及后续调优方向。",
					rid, base, rid, base,
				)
				return &GetPromptResult{
					Description: "压测瓶颈与尾延迟回归分析工作流",
					Messages:    []PromptMessage{{Role: "user", Content: ContentBlock{Type: "text", Text: text}}},
				}, nil
			},
		},
	}
}

func (s *Server) listMCPPrompts() []Prompt {
	specs := buildPrompts()
	out := make([]Prompt, 0, len(specs))
	for _, sp := range specs {
		out = append(out, sp.prompt)
	}
	return out
}

func (s *Server) getMCPPrompt(rawParams json.RawMessage) (*GetPromptResult, *rpcError) {
	in, err := decodeArgs[GetPromptParams](rawParams)
	if err != nil || strings.TrimSpace(in.Name) == "" {
		return nil, &rpcError{Code: CodeInvalidParams, Message: "prompts/get 参数无效，缺少 name"}
	}
	name := strings.TrimSpace(in.Name)
	for _, sp := range buildPrompts() {
		if sp.prompt.Name == name {
			res, rerr := sp.render(in.Arguments)
			if rerr != nil {
				return nil, &rpcError{Code: CodeInvalidParams, Message: rerr.Error()}
			}
			return res, nil
		}
	}
	return nil, &rpcError{Code: CodeInvalidParams, Message: fmt.Sprintf("未知的 MCP Prompt 模板 %q", name)}
}
