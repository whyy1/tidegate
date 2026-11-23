# Tidegate 安装、配置与使用指南

> 返回 [项目主页](../README.md) · 本指南整合源码构建、容器与反向代理部署、全量 `TIDEGATE_*` 环境变量、QPS/RPS 与 TPS 指标体系、分布式节点组网与 MCP 2.0 安全接入说明。

---

## 目录

1. [源码编译与快速启动](#1-源码编译与快速启动)
2. [Docker / Compose 与外部 TLS 部署](#2-docker--compose-与外部-tls-部署)
3. [Nginx / Caddy 反向代理配置](#3-nginx--caddy-反向代理配置)
4. [管理员账密同步、忘记密码与 2FA 扫码绑定](#4-管理员账密同步忘记密码与-2fa-扫码绑定)
5. [全量环境变量与 CLI 参数速查](#5-全量环境变量与-cli-参数速查)
6. [功能工作流：调试、编排、压测与报告导出](#6-功能工作流调试编排压测与报告导出)
7. [分布式零信任 mTLS 集群](#7-分布式零信任-mtls-集群)
8. [MCP 2.0 智能体安全接入与 16 工具权限模型](#8-mcp-20-智能体安全接入与-16-工具权限模型)
9. [导入导出生态与 CI 门禁](#9-导入导出生态与-ci-门禁)

---

## 1. 源码编译与快速启动

构建环境要求：**Go 1.26+**、**Node.js 22+ / 24+**（仅构建前端静态资源时依赖；编译后产出零外部运行时依赖的单个可执行文件）。

```bash
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=0 go build -trimpath -o tidegate ./cmd/tidegate
```

Windows PowerShell 示例：

```powershell
npm --prefix web ci
npm --prefix web run build
$env:CGO_ENABLED = "0"
go build -trimpath -o tidegate.exe .\cmd\tidegate
```

直接启动本地控制面与内置发压节点：

```bash
./tidegate
```

---

## 2. Docker / Compose 与外部 TLS 部署

仓库内置多阶段非 root 静态镜像 [Dockerfile](../deploy/Dockerfile) 与编排模板 [docker-compose.yml](../deploy/docker-compose.yml)：

```bash
# 设置管理员初始密码或重启同步密码，容器启动时自动与数据库对齐
export TIDEGATE_ADMIN_PASSWORD="<YOUR_STRONG_PASSWORD>"
docker compose -f deploy/docker-compose.yml up -d --build
```

Windows PowerShell 启动示例：

```powershell
$env:TIDEGATE_ADMIN_PASSWORD = "<YOUR_STRONG_PASSWORD>"
docker compose -f deploy/docker-compose.yml up -d --build
Remove-Item Env:TIDEGATE_ADMIN_PASSWORD
```

默认端口与持久化卷说明：

- `8080/tcp`：Web 控制台、REST API、WebSocket 遥测与 MCP Streamable HTTP（默认启用 `--tls-self-signed`，访问地址 `https://localhost:8080/`）。
- `7443/tcp`：集群双向 mTLS gRPC 通信端口。
- `/data` 持久化卷：保存 SQLite 数据库（`tidegate.db`）、AES-256-GCM 主密钥（`master.key`）、项目 YAML 及自签名证书。

如需挂载正式 TLS 证书、固定主密钥或 Docker Secrets：

```yaml
services:
  tidegate:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    environment:
      TIDEGATE_LISTEN: "0.0.0.0:8080"
      TIDEGATE_CLUSTER_LISTEN: "0.0.0.0:7443"
      TIDEGATE_CLUSTER_PUBLIC_ADDR: "tidegate.example.com:7443"
      TIDEGATE_PUBLIC_URL: "https://tidegate.example.com"
      TIDEGATE_TLS_CERT: "/certs/fullchain.pem"
      TIDEGATE_TLS_KEY: "/certs/privkey.pem"
      TIDEGATE_ADMIN_USER: "admin"
      TIDEGATE_ADMIN_PASSWORD_FILE: "/run/secrets/tidegate_admin_password"
      TIDEGATE_MASTER_KEY: "<64位十六进制或32字节Base64主密钥>"
    command: ["serve"]
```

---

## 3. Nginx / Caddy 反向代理配置

将 Tidegate 部署在 Nginx、Caddy 或云负载均衡之后时：

1. 在 Tidegate 端配置：
   - `TIDEGATE_TLS_SELF_SIGNED=false`（当由反代终结 HTTPS 时）
   - `TIDEGATE_PUBLIC_URL=https://tidegate.example.com`（自动加入可信 Origin 并修正外网展示地址）
   - `TIDEGATE_TRUSTED_PROXIES=127.0.0.1/8,10.0.0.0/8`（可信代理 IP/CIDR、`loopback` 或 `private`）
   - 若反代与 Tidegate 之间走内网明文 HTTP，可设置 `TIDEGATE_ALLOW_INSECURE_REMOTE=true`（公网入口仍必须为 HTTPS）。
2. 反代需透传 `X-Forwarded-Proto`、`X-Forwarded-Host`、`X-Forwarded-For` 与 **WebSocket Upgrade** 头：

### Nginx 示例

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl http2;
    server_name tidegate.example.com;

    ssl_certificate     /etc/nginx/certs/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Host $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 3600s;
    }
}
```

### Caddy 示例

```caddyfile
tidegate.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

> 安全说明：只有源自 `TIDEGATE_TRUSTED_PROXIES` 可信网段的连接才会采信 `X-Forwarded-*` 头，系统从右向左剥离可信代理以还原真实客户端 IP，防止客户端伪造请求头绕过限流与审计。

---

## 4. 管理员账密同步、忘记密码与 2FA 扫码绑定

Tidegate 支持**启动声明式环境变量同步**与**命令行重置**两种运维通道（登录页点击 **忘记密码？** 亦可直接一键复制以下对应命令）：

### 方式一：环境变量启动同步（推荐 Docker / 容器编排）

- 启动时设置 `TIDEGATE_ADMIN_PASSWORD` 或 `TIDEGATE_ADMIN_PASSWORD_FILE`：
  - 目标管理员（默认 `admin`，可用 `TIDEGATE_ADMIN_USER` 指定）不存在时：跳过首屏初始化，直接自动创建该管理员。
  - 目标管理员已存在时：校验环境变量密码与库内密码哈希——若一致则保持现有会话（幂等）；若不同则**立即覆盖库内密码并吊销全部历史会话与恢复令牌**。
- 若需允许后续在页面自主改密且重启不被旧 env 覆盖，改密同步后移除或清空该环境变量即可；只要环境变量保留，容器每次重启都会以环境变量与数据库保持一致为最高准则。
- 若遗失验证器，临时附加 `TIDEGATE_ADMIN_RESET_TOTP=true` 启动一次即可清空该管理员 TOTP 绑定与旧恢复码。

### 方式二：命令行离线重置 (`tidegate reset-password`)

```bash
# 容器内执行（支持交互输入或 --password 直接传入）
docker exec -it tidegate-controller \
  tidegate reset-password --data /data \
  --user admin \
  --password "<YOUR_NEW_PASSWORD>" --reset-totp

# 本地二进制直接执行
./tidegate reset-password --data ./data \
  --user admin \
  --password "<YOUR_NEW_PASSWORD>" --reset-totp
```

### 2FA 双因素绑定与恢复码导出

在右上角进入 **账号与令牌 -> 双因素认证**：

1. 点击生成绑定信息后，左侧将渲染零外联静态 QR Code 二维码（支持主流 Authenticator App 直接扫码），右侧支持分段阅读与一键复制 Base32 密钥或折叠查看 `otpauth://` URI。
2. 输入 6 位动态验证码完成激活后，系统返回一次性恢复码列表，支持**直接下载 `.txt` 备份**或一键复制保存。
3. 管理员亦可在 **系统治理 -> 用户与角色** 中对任意其他用户内联执行密码重置或 2FA 解绑。

---

## 5. 全量环境变量与 CLI 参数速查

### `tidegate serve` 参数

| 环境变量 | CLI 参数 | 默认值 | 说明 |
|---|---|---|---|
| `TIDEGATE_DATA` | `--data` | `./data` | 数据目录（YAML 项目、SQLite、`master.key`） |
| `TIDEGATE_LISTEN` | `--listen` | `127.0.0.1:8080` | Web UI / HTTP API / WebSocket / MCP 监听地址 |
| `TIDEGATE_CLUSTER_LISTEN` | `--cluster-listen` | `127.0.0.1:7443` | 集群节点 gRPC 双向 mTLS 监听地址 |
| `TIDEGATE_CLUSTER_PUBLIC_ADDR` | `--cluster-public-addr` | `""` | 对外公告集群节点接入地址（`host:port`） |
| `TIDEGATE_CLUSTER_TOKEN` | `--cluster-token` | `""` | 静态/预设集群节点加入认证令牌 |
| `TIDEGATE_MASTER_KEY` | `--master-key` | `""` | 敏感变量加密主密钥（64 位十六进制或 32 字节 Base64，优先于文件） |
| `TIDEGATE_MASTER_KEY_FILE` | `--master-key-file` | `<data>/master.key` | 敏感变量加密主密钥文件路径（不存在时自动生成） |
| `TIDEGATE_TLS_CERT` | `--tls-cert` | `""` | PEM 格式服务端 TLS 证书路径 |
| `TIDEGATE_TLS_KEY` | `--tls-key` | `""` | PEM 格式服务端 TLS 私钥路径 |
| `TIDEGATE_TLS_SELF_SIGNED` | `--tls-self-signed` | `false` | 自动签发并持久化自签名 TLS 证书 |
| `TIDEGATE_ALLOW_INSECURE_REMOTE` | `--allow-insecure-remote` | `false` | 允许非本机通过明文 HTTP 登录（仅推荐反代内网使用） |
| `TIDEGATE_PUBLIC_URL` | `--public-url` | `""` | 对外访问完整地址（如 `https://tidegate.example.com`） |
| `TIDEGATE_ALLOWED_ORIGINS` | `--allowed-origins` | `""` | 额外允许的浏览器 Origin 列表（逗号分隔） |
| `TIDEGATE_TRUSTED_PROXIES` | `--trusted-proxies` | `""` | 可信反代 IP/CIDR 或 `loopback`/`private`（逗号分隔） |
| `TIDEGATE_ADMIN_USER` | `--admin-user` | `admin` | 自动初始化或启动同步的管理员用户名 |
| `TIDEGATE_ADMIN_PASSWORD` | `--admin-password` | `""` | 管理员初始化或重启同步明文密码 |
| `TIDEGATE_ADMIN_PASSWORD_FILE` | `--admin-password-file` | `""` | 管理员密码文件路径（如 Docker Secrets） |
| `TIDEGATE_ADMIN_RESET_TOTP` | `--reset-totp` | `false` | 启动时清空目标管理员 TOTP 双因素认证绑定 |
| `TIDEGATE_NO_LOCAL_AGENT` | `--no-local-agent` | `false` | 禁用自动监督本机 `agent` 子进程 |
| `TIDEGATE_NO_OPEN` | `--no-open` | `false` | 启动后不自动打开浏览器 |
| `TIDEGATE_CLIENT` | `--client` | `fast` | 默认发压 HTTP 客户端实现：`fast` 或 `std` |

### `tidegate agent` 参数

| 环境变量 | CLI 参数 | 默认值 | 说明 |
|---|---|---|---|
| `TIDEGATE_JOIN` | `--join` | `""` | 控制器集群 gRPC 地址（`host:port`） |
| `TIDEGATE_TOKEN` | `--token` | `""` | 一次性或静默加入令牌 |
| `TIDEGATE_NAME` | `--name` | 主机名 | 发压节点显示名称 |
| `TIDEGATE_WORK_DIR` | `--work-dir` | `""` | 临时运行文件目录 |
| `TIDEGATE_CERT_DIR` | `--cert-dir` | `""` | mTLS 节点证书持久化目录（启用后重启免令牌自动续连） |
| `TIDEGATE_CA_FINGERPRINT` | `--ca-fingerprint` | `""` | 控制器集群 CA SHA-256 指纹校验值 |
| `TIDEGATE_LABELS` | `--labels` | `""` | 节点调度标签（如 `zone=cn-east,tier=perf`） |
| `TIDEGATE_AGENT_INSECURE` | `--insecure` | `false` | 禁用集群 mTLS 明文连接（仅内网测试使用） |
| `TIDEGATE_CLIENT` | `--client` | `fast` | 默认发压 HTTP 客户端实现：`fast` 或 `std` |

---

## 6. 功能工作流：调试、编排、压测与报告导出

### 界面导航、外观与全局快捷键

- **原生浅色 / 深色 / 跟随系统三态外观**：支持顶栏快捷切换及「账号 -> 外观」三态选择器，由首屏同步初始化防闪烁，图表（uPlot）与长图报告自动随当前主题着色。
- **`Ctrl+K` / `⌘K` 全局检索**：快速跳转接口、场景、环境或打开 MCP 设置。
- **TopBar 上下文条**：一键切换项目、激活环境（内联展示 `BaseURL`）以及读写全局/会话级临时变量。

### 多协议工作台与 cURL 闭环

- **协议矩阵**：支持 **HTTP/1.1 & HTTP/2**、**WebSocket**、**gRPC**（支持 Server Reflection 与 `.proto/.pb/.desc`）及 **SSE**。
- **双向 cURL 与调试回放**：支持 **粘贴 cURL 导入** 与实时 **导出为 cURL**（Bash / PowerShell、变量展开或模板、密钥脱敏开关）；点击任意历史调试记录可完整回放当时发送参数与 5 段耗时拆解（DNS / TCP / TLS / TTFB / Total），并可将非二进制文本响应直接下载或存为接口示例。
- **树形响应检视**：支持关键字及 JSONPath 检索高亮与一键展开/折叠；支持在已有接口上直接 **另存副本** 与全目录/指定目录 **批量回归测试**。
- **四级变量优先级**：会话临时变量 > 场景/单步覆写 > 环境级变量 > 项目级变量；勾选「密」即写入 AES-256-GCM Vault，界面回显与项目导出均自动脱敏。

### 场景编排与 QPS / RPS、TPS 指标口径

- **多步控制流**：支持接口引用、嵌套 `group`（事务分组）、`loop`（计数/条件循环）、`if`、`weighted` 分流与受限纯 Go `script` 步骤，可从步骤卡片一键直达编辑原接口，支持组内与顶层任意步骤克隆与折叠。
- **压测模型与口径区分**：
  - **`VU` 并发模式**：固定/分阶段模拟活跃并发用户数。
  - **`QPS / RPS` 速率模式（Open-loop）**：按目标每秒请求到达率发压，并配合 `max_vus` 防止协同省略（Coordinated Omission）失真。
  - **面板区分 `QPS / RPS` 与 `TPS`**：单接口每完成 1 次请求计 1 次 Request（汇总为 **QPS / RPS**）；每次完整场景迭代以及每个**事务分组（`group`）** 的完整闭环均单独计算 **TPS（事务/秒）**，方便在同一场景里分别评估单接口能力与整条业务链路容量。
- **多格式导出与 PNG 长图报告**：
  - **导出 PNG（2x DPR）**：单次运行与历史对比均支持纯本地高清 PNG 报告长图下载（含核心指标卡、图表走势与分步明细，自动匹配当前浅色/深色主题）。
  - 同时支持 **HTML 单文件**、**CSV**、**JSON**、**JTL** 与 **HAR 1.2**。
- **外部监控集成**：管理员可在 **系统治理 -> 指标导出** 一键复制 `prometheus.yml` 采集片段（`/api/v1/metrics/prometheus`）或配置 **InfluxDB v1/v2 / Pushgateway** 实时异步外推。

---

## 7. 分布式零信任 mTLS 集群

1. 放行控制器集群端口（默认 `7443/tcp`，容器或 NAT 后请配置 `TIDEGATE_CLUSTER_PUBLIC_ADDR`）。
2. 进入左侧 **发压节点** 页面，填写节点预设标签（如 `zone=cn-east,tier=perf`）并点击 **签发加入令牌**，页面自动拼装带有 `--join`、`--ca-fingerprint`、`--labels` 的接入命令。
3. 在远程发压机执行接入命令后，节点通过 CSR 换签将双向 mTLS 证书持久化到 `--cert-dir`，后续重启无需重传加入令牌。
4. 运行时可按节点权重（Enter 提交、Esc 还原）、带宽上限或标签选择器（`node_selector`）精细调度，并支持在线吊销节点证书或切换掉线重平衡策略。

---

## 8. MCP 2.0 智能体安全接入与 16 工具权限模型

Tidegate 内置无外部依赖的纯 Go MCP 2.0 服务端，同时支持 **Streamable HTTP（`/mcp`）** 与 **CLI Stdio（`./tidegate mcp`）**，通过多层次治理确保智能体操作透明且可控：

### 四大分类与 16 个工具全景

| 分类 | 包含工具 | 默认 Scope | 说明 |
|---|---|---|---|
| **只读探查（9）** | `list_projects`、`get_project_workspace`、`get_api_detail`、`get_scenario_detail`、`estimate_load_run`、`list_runs`、`get_run_report`、`compare_runs`、`get_cluster_status` | `read` | 浏览项目结构、脱敏接口详情、容量预估、压测报告与节点状态 |
| **资产编排（3）** | `save_api`、`import_api_spec`、`save_scenario` | `write` | 创建/更新接口与场景；其中 `import_api_spec` 的 `mode=preview` 仅需 `read` 权限，实际写入需要 `write` |
| **调试回归（3）** | `debug_api`、`debug_scenario`、`run_collection_test` | `debug` | 对外发起单接口、场景试跑与目录批量回归 |
| **压测控制（1）** | `manage_load_run` | `run` | 受控发起压测（`start`）、在线调压（`set_target`）、`graceful_stop` 与 `abort`（破坏性/外部访问受护栏限制） |

### 权限四重防线

1. **管理员全局护栏（系统治理 -> 安全护栏）**：可开关 MCP 总开关、全局禁用 AI 发起或控制压测（`MCPDisableLoadRuns`，统一拦截 `start` / `set_target` / `graceful_stop` / `abort`）、通过 16 工具独立矩阵按单工具粒度精准开关（`mcp_disabled_tools`），支持一键「仅限只读」快速收口；硬上限默认 `200 VUs / 300s`，支持出站目标域名/IP 白名单（SSRF 防护）。
2. **Scoped API 令牌与工具白名单（账号与令牌）**：创建令牌时按 `mcp` + `read` / `write` / `debug` / `run` 勾选 Scope，可即时预览 16 个工具的解锁状态，并可选绑定「限定项目」与「限定 MCP 工具」细粒度白名单（`mcp_tools`）。
3. **密钥零出域**：Vault 加密变量与认证敏感请求头在返回智能体前均脱敏处理。
4. **完整可追溯审计**：每次 MCP 工具调用均记录调用方身份、参数摘要与结果，在 **系统治理 -> 审计日志** 可按前缀胶囊筛选、展开完整 JSON 并导出 CSV。

### 通用 Streamable HTTP 客户端配置示例

```json
{
  "mcpServers": {
    "tidegate": {
      "url": "https://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_API_TOKEN>"
      }
    }
  }
}
```

> 本地 Stdio 模式可通过控制台「账号与令牌 -> MCP 接入」直接生成并复制对应 JSON。

---

## 9. 导入导出生态与 CI 门禁

- **两步预览导入**：在工作台点击 **导入**，支持 OpenAPI 3.0/3.1、Swagger 2.0、Postman v2.1、Apifox、JMeter（`.jmx`）、HAR 1.2、cURL 与 `.tidegate` 归档。选择智能合并时先预览差异 diff，写入时自动保留团队手工添加的断言、提取器与前后置脚本。
- **生态导出**：项目管理弹窗支持导出 `.tidegate` 完整归档、OpenAPI 3.0（YAML/JSON）与 Postman v2.1；场景页面支持导出独立 **k6 脚本（`.js`）**。
- **CI/CD 质量与性能门禁**：

```bash
# 目录接口功能回归（任一步骤失败返回退出码 3）
./tidegate test --data ./data --folder "订单核心链路" --env "预发环境" --junit junit-report.xml

# YAML 场景压测与 SLA 阈值校验（违约返回退出码 3）
./tidegate run examples/ping.yaml --out summary.json --csv stats.csv --html report.html --jtl result.jtl
```
