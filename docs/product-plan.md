# Tidegate 产品架构与功能白皮书

版本：v1.1 正式版　更新日期：2026-11-23

## 1. 一句话定位

Tidegate 是一个本地优先、可离线单文件部署的 API 调试与分布式压测一体化控制台：在同一个双轴 IDE 工作台里完成多协议接口定义、前后置脚本与批量回归测试、复杂控制流场景编排，随后直接调度单机或多机零信任 mTLS 集群发起压测，通过 HDR 直方图与硬件双层遥测精准定位瓶颈，并通过安全受控的 MCP 2.0（Model Context Protocol）协议向 AI 助手开放受项目级 ACL 与 Scope 约束的只读资源、诊断模板与护栏保护下的自动化编排。

---

## 2. 解决的核心痛点

| 现有痛点 | 根因 | Tidegate 的架构方案 |
| :--- | :--- | :--- |
| 压测时界面卡顿、内存暴涨 | 传统工具界面与发压线程同进程且每请求渲染 | 控制面与发压引擎彻底进程隔离；引擎按 Go 协程运行，仅每秒上报聚合 HDR 直方图与少量采样 |
| 接口管理与压测工具割裂 | 调试工具重交互、压测工具重 CLI，接口需维护两份 | 目录树与 YAML 接口定义作为唯一数据源，接口更新后场景与压测配置自动同步引用 |
| 分不清服务端瓶颈还是发压机瓶颈 | 仅展示表面 QPS，忽略发压机 NIC 带宽与宿主机资源 | 同步采集应用层流量与操作系统网卡速率/CPU/内存/临时端口，饱和时自动实时告警并标注不可信区间 |
| 单机上行或端口受限难以扩展 | 缺少原生分布式协议或部署过于笨重 | 内置零信任 ECDSA CA 与双向 mTLS gRPC 集群，一条命令加入节点，原生支持权重、标签筛选与掉线重平衡 |
| AI 工具接入接口平台存在密钥与生产冲垮风险 | 缺乏细粒度 RBAC 脱敏与并发硬护栏 | 内置纯 Go 安全 MCP 2.0 网关：强制 Vault 密钥零出域、RBAC/Scope/项目 ACL 隔离、SSRF 白名单校验、200 VUs / 300s 默认硬护栏与全量审计 |

---

## 3. 核心设计原则

1. **热路径零阻塞**：引擎运行前将变量模板、提取器与控制流预编译为确定性执行计划，热路径复用对象缓冲（支持空闲缓冲收缩）且无锁统计 HDR 直方图。
2. **界面永不阻塞发压**：即使发压节点 CPU/网卡打满，Web 控制台与操作响应依旧保持流畅。
3. **单机即单节点集群**：默认 `serve` 自动监督本机 `agent` 进程，单机压测与多机分布式压测共享完全一致的调度与直方图合并路径。
4. **安全内建（Security by Default）**：所有资源零外部 CDN/字体外联（严格 `default-src 'self'` CSP）、AES-256-GCM 敏感变量保险箱、TOTP 双因素认证、项目级 RBAC 访问控制、令牌 Scope 隔离、出站目标白名单与全链路审计日志默认集成。
5. **本地优先与开放互通**：项目数据以标准 YAML 文件落盘，可直接纳入 Git；完整支持 OpenAPI 3 / Swagger 2 / Postman / Apifox / JMeter / HAR / cURL / k6 / JUnit XML 导入导出与智能非破坏性合并。

---

## 4. 总体架构

```mermaid
flowchart LR
    UI["双轴 IDE Web 工作台\n(React + uPlot + WS)"] -- HTTPS / WebSocket --> C["控制器 Controller\n(认证 · ACL · 调度 · 合并)"]
    AI["AI / IDE 客户端\n(Streamable HTTP / Stdio)"] -- "Bearer tg_... + Scope/MCP 护栏" --> MCP["安全 MCP 2.0 网关 (/api/v1/mcp)"]
    MCP --> C
    CLI["无界面 CLI\n(tidegate run / test / mcp)"] --> C
    C -- YAML 文件读写 --> P[("项目库 YAML + 数据文件")]
    C -- SQLite 读写 --> R[("结果库 · Vault · 2FA/ACL · 审计日志")]
    A1["发压节点 Agent (本机/远程)"] <-- "mTLS 双向 gRPC 流" --> C
    A2["发压节点 Agent B"] <-- "mTLS 双向 gRPC 流" --> C
    A1 --> T["被测目标 (受 SSRF 白名单约束)"]
    A2 --> T
```

单一原生二进制内置七种运行角色：

| 命令 | 角色 | 说明 |
| :--- | :--- | :--- |
| `tidegate serve`（默认） | 控制台 + 控制器 + 本机发压节点 + MCP HTTP 端点 | 启动完整控制台并自动监督本机 `agent` 子进程；支持 `--tls-self-signed` 与反代模式 |
| `tidegate agent` | 分布式发压节点 | 通过一次性 Join Token 与 `--ca-fingerprint` 完成双向 mTLS 注册，并可凭 `--cert-dir` 免密续连 |
| `tidegate mcp` | 本地 CLI Stdio MCP 代理 | 将 `stdin`/`stdout` JSON-RPC 2.0 安全桥接至运行中的 Tidegate 服务 |
| `tidegate test` | 无界面功能回归与 JUnit 报告 | 顺序执行目录/项目接口（含前后置脚本与断言），输出 JSON 与 `--junit` XML，失败返回退出码 `3` |
| `tidegate run <场景文件>` | 无界面压测与 CI 门禁 | 支持导出 JSON/CSV/HTML/JTL，SLA 阈值违约时以退出码 `3` 中止流水线 |
| `tidegate reset-password` | 管理员密码重置 | 离线运维命令，安全重置指定用户密码并轮换会话 |
| `tidegate testserver` | 内置高性能被测靶场 | 提供 HTTP / TLS / HTTP/2 / WS / gRPC（含 Server Reflection）/ SSE 基准与回归测试端点 |

---

## 5. 功能模块全景（`v1.1` 已全面交付）

### 5.1 双轴 IDE 工作台与接口管理
- **应用骨架**：深海潮闸蓝可折叠导航栏（`SidebarRail`）+ 44px 上下文顶栏（含激活环境 `BaseURL` 预览胶囊与后台运行态迷你遥测胶囊）+ `Ctrl+K / ⌘K` 原生 `<dialog>` 全局命令与接口检索面板。
- **可拖拽分屏与高效编辑**：支持**左右并排**（宽屏默认）与**上下分屏**实时切换，支持鼠标拖拽与键盘方向键精确调节比例并自动记忆偏好；内置 `{{var}}` 四级变量实时自动补全（`VariableInput`）与带行号前后置脚本编辑器（`CodeEditor`）。
- **多协议调试与前后置脚本**：支持 **HTTP/1.1、HTTP/2、WebSocket、gRPC Unary（支持 Server Reflection `v1`/`v1alpha` 与 `.proto` / `FileDescriptorSet` 自动发现及示例 JSON 生成）、SSE**；支持接口级 `pre_script` / `post_script`（提供 `resp.status` / `resp.body` / `resp.header` / `resp.json` 与 `jsonpath`）、路径/查询参数、请求头继承、多形态请求体、鉴权（Bearer / Basic / API Key / OAuth 2.0 Client Credentials）、XPath / JSONPath / 正则提取器、JSON Schema 断言与 OpenAPI 响应示例/字段文档。
- **目录批量回归与调试历史**：工作台与 CLI（`tidegate test`）均支持按目录或全项目顺序运行功能回归测试（含跨接口变量传递、首错即停与结果明细）；每次单次调试捕获五段耗时瀑布并强制脱敏落库。
- **四级变量与 Vault**：支持项目、环境、场景及会话临时变量池四级覆写与 `{{func}}` 动态内置函数；`Secret: true` 变量由独立主密钥以 AES-256-GCM 分域加密存储，不进入导出归档与明文日志。

### 5.2 场景编排与确定性发压引擎
- **流程控制与韧性引擎**：支持事务分组（`group`）、计数/条件循环（`loop`）、分支判断（`if`）、加权分流（`weighted`）及纯 Go 受限 AST 脚本步骤（`script`，管理员可全局禁用）；支持 `on_error` 细粒度错误策略（`continue` / `abort_iteration` / `abort_vu`）、无锁 `max_dials_per_sec` 建连速率限制与连接空闲时按阈值自动收缩读取缓冲（`shrinkIdleBuffers`）。
- **数据驱动与混合流量**：支持 CSV / JSON / JSONL 参数化数据文件与跨分布式节点分片（`slice`）；支持多场景加权混合（`Mix`）、思考时间（固定/均匀/正态分布）与自定义代理/DNS 覆写。
- **双负载模型与 SLA 阈值**：支持闭环并发（VUs）与开环固定速率（RPS），多阶段（预热/爬坡/稳态/退坡）编排；支持运行期在线调压与开压前吞吐/带宽容量预估；SLA 阈值违约支持自动中止并生成诊断结果。

### 5.3 实时可观测、HDR 统计与报告对比
- **实时多维大盘**：1Hz 聚合推送，内置按需拆包加载的 `uPlot` 吞吐量、延迟分位（P50/P90/P95/P99/Max）、错误率、并发、双层带宽及告警区间高亮。
- **深度延迟剖面**：基于精确 HDR 直方图实现跨节点无损归并，支持 12 段耗时分布柱状图、时间×延迟分桶热力图与异常请求一键工作台复现。
- **对比与可观测导出**：支持 2–6 次历史压测叠加与基线回归分析；内置 Prometheus 采集端点（`/api/v1/metrics/prometheus`）、InfluxDB v1/v2 与 Prometheus Pushgateway 异步实时外推（带防云元数据 SSRF Dial 校验），并支持导出离线单文件 HTML、CSV、JSON、JUnit XML、HAR 1.2 与 JMeter JTL。

### 5.4 多格式导入导出生态与智能合并
- **两步预览与非破坏性智能合并**：先预览创建/更新/跳过差异 diff 与受保护保留字段（如既有的断言、提取器、前后置脚本与接口描述），在 `merge` 模式下精准合入新增或变更接口而不覆盖团队手工补充的调试资产；支持 `.tidegate` 归档、OpenAPI 3.x / Swagger 2.0、Postman Collection v2.1（含环境）、Apifox、HAR 1.2、JMeter `.jmx` 与 cURL 命令。
- **全面生态导出**：支持导出 `.tidegate` 完整归档、OpenAPI 3.0 JSON/YAML（含示例与响应字段文档）、Postman v2.1 与 **k6 JavaScript 压测脚本**（密文变量保留模板占位不泄露）。

### 5.5 零信任分布式 mTLS 集群
- **零信任自建 CA**：控制器自动生成 ECDSA P-256 集群 CA，通过一次性/限时 Join Token + `--ca-fingerprint` SHA-256 指纹完成发压节点 CSR 换签与双向 mTLS 认证。
- **证书持久化与节点治理**：支持 `--cert-dir` 节点证书自动续连、危险节点在线吊销隔离、节点标签筛选（`node_selector`）、自定义权重按比例分流及 `redistribute` / `abort` 节点掉线容错策略。

### 5.6 安全与企业级治理
- **账号、双因素认证（2FA）与凭证**：首次启动一次性初始化口令、Argon2id 抗暴力破解存储、RFC 6238 TOTP 双因素认证与 8 组一次性恢复码（支持管理员一键重置）、多端会话列举与踢出、`HttpOnly + SameSite=Strict` 会话；长期 `tg_` Bearer 令牌支持细粒度能力 `scopes`（`read`、`write`、`debug`、`run`、`mcp`、`admin`）与 `project_ids` 项目范围绑定，兼容无 Scope 历史令牌。
- **项目级 RBAC 访问控制（ACL）与护栏**：在全局 `admin` / `member` / `viewer` 角色之上新增项目级私有隔离开关与每用户角色覆写（`restricted` + `member` / `viewer` 授权）；支持出站域名/IP/CIDR 目标白名单（防重绑定拨号校验）、成员 VUs/RPS 配额、全局脚本禁令与 `audit_logs` 操作审计。

---

## 6. MCP 2.0 智能体安全协议接入（Model Context Protocol）

Tidegate 内置无外部依赖的纯 Go MCP 2.0 服务端，兼容 Claude Desktop、Cursor 等主流 MCP 宿主客户端：

1. **双模式传输**：
   - **Streamable HTTP**：`POST /api/v1/mcp`（遵循 MCP `2025-06-18` / `2025-03-26` JSON-RPC 2.0，使用 `Authorization: Bearer tg_...` 认证并强制校验 `mcp` Scope，无凭证探测规范返回 `401`）。
   - **CLI Stdio 代理**：`tidegate mcp --url <URL> --token tg_...`（所有日志写往 `stderr`，`stdout` 保持纯净 JSON-RPC 行帧）。
2. **16 个高内聚工具矩阵（全部标注安全 Annotations 与所需 Scope）**：
   - **只读探查与诊断（`viewer`+ / `read`）**：`list_projects`、`get_project_workspace`、`get_api_detail`、`get_scenario_detail`、`estimate_load_run`、`list_runs`、`get_run_report`、`compare_runs`、`get_cluster_status`。
   - **编排与导入（`member`+ / `write`）**：`save_api`、`import_api_spec`（支持 `preview`/`apply`）、`save_scenario`。
   - **受控调试、集合回归与压测（`member`+ / `debug` & `run`，`openWorldHint: true`）**：`debug_api`、`debug_scenario`、`run_collection_test`、`manage_load_run`（`start`/`set_target`/`graceful_stop`/`abort`）。
3. **MCP Resources 与 Prompts 模板**：
   - **只读 Resources & 模板（需 `read` Scope，强制脱敏）**：暴露 `tidegate://projects` 与模板 `tidegate://projects/{project_id}/workspace`、`tidegate://projects/{project_id}/environments`、`tidegate://runs/{run_id}/summary`。
   - **专家 Prompts 模板（4 项）**：`diagnose_api_failure`、`generate_regression_suite`、`design_load_scenario`、`analyze_run_bottleneck`。
4. **七层纵深安全防线**：
   - 管理员策略开关（`mcp_disabled` 总开关、`mcp_disable_load_runs` 禁止 AI 发起压测）；
   - RBAC 角色 + 令牌 `scopes` + `project_ids` + 项目私有 ACL 四重权限裁剪；
   - 强制零密钥出域（`IncludeSecrets=false` + `RedactSecrets=true` + 敏感头掩码）；
   - 出站 SSRF 白名单与 `scripts_disabled` 透传校验；
   - 默认单次压测硬护栏（默认上限 **200 VUs / 300 秒**，防止误调度超大规模流量）、请求 `1 MiB` 与响应 `48 KiB` 截断、单 actor 120 req/min 限流；
   - 全量 MCP 调用写入 `audit_logs`（`action: "mcp.tool.<name>"`）。

---

## 7. 技术选型与性能指标

| 层级 | 技术栈选型 | 说明 |
| :--- | :--- | :--- |
| 核心引擎与控制器 | **Go 1.26+**（`CGO_ENABLED=0` 静态编译） | 自研高性能 HTTP/1.1 引擎 + HTTP/2 / WebSocket / gRPC / SSE 客户端；纯 Go AST 脚本沙箱 |
| 界面前端 | **React 18 + TypeScript + Tailwind CSS v4 + uPlot** | 自研精密测控台设计令牌与双轴 IDE 分屏工作台，构建产物按需拆包后通过 `go:embed` 内嵌二进制 |
| 存储与直方图 | YAML 项目目录 + 纯 Go SQLite (`modernc.org/sqlite`) + HDR 直方图 | 支持 AES-256-GCM 主密钥保险箱、2FA/ACL 关系表与跨节点直方图合并 |
| 容器与跨平台 | 多阶段静态镜像（非 root + 只读根文件系统） | 原生支持 Windows、Linux、macOS |
