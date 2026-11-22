<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-wordmark-dark.svg">
    <img src="assets/logo-wordmark.svg" alt="Tidegate" height="72">
  </picture>
</p>

<p align="center">本地优先的 API 管理与分布式压测一体化平台（含安全 MCP 智能体接入）</p>

---

Tidegate 把接口管理和压测放进同一个工具：在双轴 IDE 风格工作台里管理接口、分屏调试、编排场景，然后直接发起单机或多机分布式压测，同时观测实时图表和发压机自身的硬件与网卡瓶颈；也可通过内置的安全 MCP Server 让 AI 编程助手协作诊断压测报告与编排接口。

数据都在你自己的机器上，不需要云端账号，不加载任何远程代码。

## 名字的由来

Tidegate 是潮汐闸门的意思。

压测的流量像潮水，有涨有落：爬坡是涨潮，保持是满潮，下降是退潮。闸门决定放多少水过去、什么时候放。Tidegate 做的就是这件事：按你设定的节奏把流量放向被测系统，再量出它能承受多大的潮。

logo 是一道抬起的闸门，形状正好是字母 T，潮水从闸下流过。

## 它解决什么问题

- **接口管理和压测不用再换工具。** 接口定义只有一份，调试、场景、压测都引用它，接口改了场景自动跟着变。在接口页面可以一键发起压测。
- **压测时界面不卡。** 界面和发压引擎是两个进程，引擎只上报每秒聚合后的指标，把 CPU 打满也不影响界面操作。
- **知道瓶颈在哪一边。** 实时显示每台发压机的带宽占用、CPU、内存和端口。发压机自己成为瓶颈时给出告警，并把这段结果标记为不可信。
- **单机不够就加机器。** 内置分布式集群，在其他机器上运行一条命令就能加入，多机合并发压，指标自动合并。
- **AI 助手安全可审计介入。** 内置纯 Go MCP Server，强制 RBAC 权限隔离、Vault 密钥零出域、SSRF 白名单校验与单次压测规模硬护栏。

## 功能

| 模块 | 内容 |
|---|---|
| 接口管理与 IDE 工作台 | 可折叠双轴导航、`Ctrl+K` 全局命令面板、左右/上下可拖拽分屏工作台；项目与目录树、多环境与临时变量池、AES-256-GCM 密钥保险箱；支持 HTTP/1.1 & HTTP/2、WebSocket、gRPC、SSE 调试、XPath/JSONPath/正则提取、JSON Schema 断言与响应文档 |
| 场景编排 | 步骤覆盖、事务分组 (`group`) / 循环 (`loop`) / 条件 (`if`) / 权重分流 (`weighted`) 与纯 Go 受限沙箱脚本、固定/均匀/正态思考时间、CSV/JSON/JSONL 数据驱动、多场景加权 Mix 与代理/DNS 覆写 |
| 压测配置 | 闭环并发（VUs）与开环速率（RPS）双模式，预热、爬坡、保持、下降阶段编排与 SLA 违约自动中止阈值 |
| 实时图表与观测 | `uPlot` 吞吐量、延迟百分位、并发、双层带宽与瓶颈区间标注；HDR 延迟直方图、时间热力图、五段耗时分解与分节点明细；支持 `/api/v1/metrics/prometheus` 采集及 InfluxDB / Prometheus Pushgateway 实时外发 |
| 安全与治理 | 多角色 RBAC（`admin` / `member` / `viewer`）、`tg_` API 访问令牌与限流防护、出站目标 Host/CIDR 白名单与成员并发/RPS 上限、全链路操作审计日志与调试历史自动脱敏 |
| MCP 智能体接入 | Streamable HTTP (`POST /api/v1/mcp`) 与 CLI Stdio (`tidegate mcp`) 双传输、15 个读写分离且带完整安全注解的工具、Vault 零泄露与默认 200 VUs / 300s 护栏 |
| 报告与对比 | 历史记录、异常采样一键工作台复现、离线单文件 HTML 报告、CSV / JSON / JTL / HAR 导出、2–6 次多次基线与分步骤回归对比 |
| 分布式集群 | 集群 ECDSA CA 与双向 mTLS、Join Token 换签与证书持久化重连、`--ca-fingerprint` 指纹校验、节点吊销、标签筛选 (`node_selector`)、自定义权重与断线自动重平衡 / 中止策略 |
| 导入导出 | 两步预览导入 `.tidegate`、OpenAPI 3.x / Swagger 2.0、Postman v2.1、Apifox、JMeter (`.jmx`)、HAR 1.2、cURL；支持导出 k6 (`.js`)、JMeter (`.jtl`)、OpenAPI 与 Postman |
| 部署 | 单文件原生可执行文件与多阶段非 root 只读容器镜像，支持 Windows、Linux、macOS |

完整的功能架构白皮书见[产品架构与功能白皮书](docs/product-plan.md)。

## 当前进度

目前全阶段能力（**双轴 IDE Web 工作台**、**多协议调试与复杂场景控制流**、**全链路可观测与对比**、**安全加固与 RBAC 治理**、**零信任 mTLS 集群** 以及 **MCP 智能体网关**）均已完整实现并集成至本地与容器交付物中：

- **双轴 IDE 工作台与多协议接口管理**：采用左侧可折叠深海导航栏、44px 上下文顶栏、`Ctrl+K` 全局快捷搜索面板与左右/上下可拖拽分屏调试区；支持本地账户（初始化口令 + Argon2id）、RBAC、多设备会话管理与 `tg_` Bearer 令牌；四级变量与 AES-256-GCM 密文保险箱、两步预览导入导出及 HTTP/1.1、HTTP/2、WebSocket、gRPC、SSE 五段耗时瀑布图。
- **场景编排与可观测大盘**：支持控制流、受限纯 Go 脚本沙箱、CSV/JSON/JSONL 数据切片与 `Mix` 混合流量；支持 HDR 直方图与时间×延迟热力图、2–6 次基线回归对比、k6/JTL/HTML/HAR 导出，以及 Prometheus 采集与 InfluxDB/Pushgateway 异步外发。
- **分布式集群与纵深安全体系**：控制器内置集群 CA、Join Token、`--ca-fingerprint`、`--cert-dir`、故障重分配；后端调试历史与外部指标推送均配备自动密文脱敏及云元数据防 SSRF 拦截；全部资源变更与 MCP 工具调用均记录 `audit_logs`。
- **安全 MCP Server（15 工具）**：同时支持 HTTP 直连与 `tidegate mcp` Stdio 桥接，可在 Web 控制台账号弹窗一键生成客户端接入配置。

### 快速开始（Web 控制台）

从源码构建前后端合一的可执行文件（需要 Go 1.26+ 与 Node.js 22+/24+）：

```bash
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=0 go build -o tidegate ./cmd/tidegate
./tidegate
```

首次启动会在终端输出管理地址（默认 `http://127.0.0.1:8080/`）与一次性初始化码，按页面引导创建管理员密码后即可使用。若忘记管理员密码，可通过命令行重置：

```bash
./tidegate reset-password --data ./data --user admin
```

### 命令行压测与 CI 门禁

无需界面时，可直接用 CLI 运行 YAML / JSON 场景并接入持续集成检查：

```bash
./tidegate testserver                                                   # 启动内置的被测服务（默认 :18080）
./tidegate run examples/ping.yaml --out result.json --csv result.csv --html report.html --jtl result.jtl
```

场景中配置的 `thresholds` 在压测结束或运行期违约校验失败时，`tidegate run` 将返回退出码 `3`，便于流水线直接作为性能门禁拦截回归；更多场景写法见 [examples](examples) 目录。

### 多机分布式压测（零信任 mTLS）

在控制器节点启用集群监听端口，并在 Web 控制台「发压节点」页面签发加入令牌：

```bash
./tidegate serve --listen 0.0.0.0:8080 --cluster-listen 0.0.0.0:7443 --tls-self-signed
```

在其他发压机上携带加入令牌、CA 指纹与节点标签首次注册接入集群（后续重启凭 `--cert-dir` 内持久化证书即可免令牌重连）：

```bash
./tidegate agent --join <控制器IP:7443> --token <加入令牌> --cert-dir ./agent-certs --ca-fingerprint <CA_SHA256指纹> --labels zone=cn-east,tier=perf
```

### MCP 智能体安全接入（Model Context Protocol）

先在控制台右上角（或侧栏「MCP 智能体接入」）生成一枚 `tg_` 访问令牌（只读排查推荐 `viewer`，编排与调试推荐设置有效期的 `member`），再按需选择 **Streamable HTTP** 或 **CLI Stdio** 模式配置客户端（如 Cursor / Claude Desktop / Antigravity `mcp_config.json`）：

**方式一：Streamable HTTP 直连（推荐）**

```json
{
  "mcpServers": {
    "tidegate": {
      "serverUrl": "https://127.0.0.1:8080/api/v1/mcp",
      "headers": {
        "Authorization": "Bearer tg_your_token_here"
      }
    }
  }
}
```

**方式二：CLI Stdio 桥接**

```json
{
  "mcpServers": {
    "tidegate": {
      "command": "tidegate",
      "args": ["mcp", "--url", "https://127.0.0.1:8080", "--token", "tg_your_token_here"]
    }
  }
}
```

- **工具能力（共 15 个）**：
  - 只读探查与分析（`viewer`+）：`list_projects`、`get_project_workspace`、`get_api_detail`、`get_scenario_detail`、`estimate_load_run`、`list_runs`、`get_run_report`、`compare_runs`、`get_cluster_status`
  - 编排与受控执行（`member`+）：`save_api`、`import_api_spec`（`preview`/`apply`）、`save_scenario`、`debug_api`、`debug_scenario`、`manage_load_run`（`start`/`set_target`/`graceful_stop`/`abort`）
- **安全护栏**：`viewer` 令牌只允许执行只读工具；所有密文变量与认证请求头在返回前自动打码；调试与压测强制遵循目标白名单与 `scripts_disabled`；MCP 发起的压测默认限制为 **单次最高 200 VUs、最长 300 秒**，管理员可在「系统治理 -> 安全围栏与 MCP」中一键开关或调整上限，所有调用均记入 `audit_logs`。

### Docker 部署

仓库内置多阶段 [Dockerfile](deploy/Dockerfile) 与开箱即用的 [docker-compose.yml](deploy/docker-compose.yml)（非 root 运行并启用只读根文件系统）：

```bash
docker compose -f deploy/docker-compose.yml up -d --build
docker logs tidegate-controller   # 查看首次启动的一次性初始化码与自签名 TLS 证书指纹
```

默认以自签名 HTTPS 监听 `https://localhost:8080/`（集群通信端口 `7443`，数据持久化卷挂载于 `/data`）。若置于 HTTPS 反向代理之后，可在 `docker-compose.yml` 中配置 `command: ["serve"]`、`TIDEGATE_ALLOW_INSECURE_REMOTE=true` 与 `TIDEGATE_ALLOWED_ORIGINS`；在 Windows 上作为远程发压机时推荐直接运行原生可执行文件 `./tidegate agent`。

## 许可

Copyright (c) 2026 whyy1

源码公开，但不是开源软件。本项目采用 [PolyForm Strict License 1.0.0](LICENSE)：

- 允许个人学习、研究和其他非商业用途的使用。
- 不允许商业使用，不允许分发，不允许修改后再发布。
- 需要商用或分发授权，请在 [Issues](https://github.com/whyy1/tidegate/issues) 里联系作者。

以 [LICENSE](LICENSE) 的英文原文为准。
