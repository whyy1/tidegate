<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-wordmark-dark.svg">
    <img src="assets/logo-wordmark.svg" alt="Tidegate" height="72">
  </picture>
</p>

<p align="center">本地优先的 API 管理与压测一体化工具</p>

---

Tidegate 把接口管理和压测放进同一个工具：在一个界面里管理接口、调试接口、编排场景，然后直接发起单机或多机压测，同时看到实时图表和发压机自身的带宽占用。

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
- **数据在自己手里。** 项目是本地的文本文件，可以用 Git 管理。自己部署，本地账户登录。

## 功能

下面是完整版的功能范围，目前实现到哪一步见“当前进度”。

| 模块 | 内容 |
|---|---|
| 接口管理 | 项目与目录树、多环境、多级变量与 AES-256-GCM 密钥保险箱、Bearer / Basic / API Key / OAuth 2.0 鉴权；支持 HTTP / WebSocket / gRPC / SSE 多协议调试、XPath / JSONPath / 正则提取、JSON Schema 断言与响应示例文档 |
| 场景编排 | 步骤覆盖、事务分组 (`group`) / 循环 (`loop`) / 条件 (`if`) / 权重分流 (`weighted`) 与受限沙箱脚本、固定 / 均匀 / 正态分布思考时间、CSV / JSON / JSONL 数据驱动、多场景加权 Mix 与 HTTP/2 / 代理 / DNS 覆写 |
| 压测配置 | 闭环并发（VUs）与开环速率（RPS）双模式，预热、爬坡、保持、下降阶段编排与 SLA 违约自动中止阈值 |
| 实时图表与观测 | `uPlot` 吞吐量、延迟百分位、并发、双层带宽与瓶颈区间标注；HDR 延迟直方图、时间热力图、五段耗时分解与分节点明细；支持 `/api/v1/metrics/prometheus` 采集及 InfluxDB / Prometheus Pushgateway 实时外发 |
| 安全与治理 | 多角色 RBAC（`admin` / `member` / `viewer`）、`tg_` API 访问令牌、出站目标 Host/CIDR 白名单与成员并发/RPS 上限、操作审计日志 |
| 报告与对比 | 历史记录、异常采样一键工作台复现、离线 HTML 报告、CSV / JSON / JTL / HAR 导出、2–6 次多次基线与分步骤回归对比 |
| 发压机监控 | 带宽占用、CPU、内存、端口、TCP 重传率，发压机饱和时自动告警并标注可信区间 |
| 分布式集群 | 集群 ECDSA CA 与双向 mTLS、Join Token 换签与证书持久化重连、`--ca-fingerprint` 指纹校验、节点吊销、标签筛选 (`node_selector`)、自定义权重与断线自动重平衡 / 中止策略 |
| 导入导出 | 两步预览导入 `.tidegate`、OpenAPI 3.x / Swagger 2.0、Postman v2.1、Apifox、JMeter (`.jmx`)、HAR 1.2、cURL；支持导出 k6 (`.js`)、JMeter (`.jtl`)、OpenAPI（含示例与字段文档）与 Postman |
| 部署 | 单文件原生可执行文件与多阶段非 root 只读容器镜像，支持 Windows、Linux、macOS |

完整的功能设计见[产品规划与功能设计](docs/product-plan.md)。

## 当前进度

目前全阶段能力（**接口管理与多协议工作台**、**复杂场景控制流与 Mix 混合**、**全链路观测与对比大盘**、**RBAC 权限与安全围栏**、**零信任 mTLS 分布式集群** 与 **Docker 部署**）均已完整实现并通过多节点端到端验收：

- **Web 控制台与多协议接口工作台**：本地账户初始化（一次性初始化码 + Argon2id）、多角色 RBAC（`admin`/`member`/`viewer`）、多设备会话注销与长期 `tg_` API 访问令牌；项目 / 环境 / 会话级临时变量池与 AES-256-GCM 敏感变量保险箱；目录树拖拽排序与两步安全导入导出（`.tidegate`、OpenAPI 3.x / Swagger 2.0、Postman v2.1、Apifox、JMeter `.jmx`、HAR 1.2、cURL）；支持 HTTP/1.1 & HTTP/2、WebSocket、gRPC Unary 与 SSE 调试、OAuth 2.0 客户端凭证鉴权、XPath 与 JSONPath 提取、JSON Schema 断言、响应示例/字段文档、五段耗时瀑布条与“一键压测”。
- **复杂场景编排与发压引擎**：支持嵌套 `group` / `loop` / `if` / `weighted` 流程控制、受限纯 Go 沙箱脚本、固定/均匀/正态分布思考时间、CSV/JSON/JSONL 参数化与多场景权重 `Mix`；支持代理、自定义 DNS 覆写、开压前容量预估、VUs/RPS 在线动态调压与 SLA 违约自动中止。
- **可观测大盘与生态导出**：支持 `uPlot` 多维趋势图、HDR 直方图与时间×延迟分桶热力图、五段网络耗时分解、分节点实况视图及 2–6 次历史基线对比；支持 k6 (`.js`) 脚本导出、JMeter (`.jtl`)、离线单文件 HTML、CSV、JSON、HAR 1.2 报告，以及 Prometheus 采集端点与 InfluxDB/Pushgateway 实时外发。
- **零信任 mTLS 集群与系统治理**：控制器内置集群 CA 与 Join Token 签发、发压节点 CSR 换签、`--ca-fingerprint` 校验、`--cert-dir` 免密续连、节点吊销隔离、标签筛选（`node_selector`）、自定义权重与断线自动重平衡/中止容错；内置目标 Host/CIDR 白名单、成员规模上限及操作审计日志面板。

### 快速开始（Web 控制台）

从源码构建前后端合一的可执行文件（需要 Go 1.26+ 与 Node.js 22+/24+）：

```bash
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=0 go build -o tidegate ./cmd/tidegate
./tidegate
```

首次启动会在终端输出管理地址（默认 `http://127.0.0.1:8080/`）与一次性初始化码，按页面引导创建管理员密码后即可使用，系统会自动创建「默认项目」。若忘记管理员密码，可通过命令行重置：

```bash
./tidegate reset-password --data ./data --user admin
```

### 命令行压测与 CI 门禁

无需界面时，可直接用 CLI 运行 YAML / JSON 场景并接入持续集成检查：

```bash
./tidegate testserver                                                   # 启动内置的被测服务（默认 :18080）
./tidegate run examples/ping.yaml --out result.json --csv result.csv --html report.html --jtl result.jtl
```

场景中配置的 `thresholds` 在压测结束或运行期违约校验失败时，`tidegate run` 将返回退出码 `3`，便于流水线直接作为性能门禁拦截回归；亦可通过账号弹窗签发 `tg_` 前缀个人 API 令牌携带 `Authorization: Bearer tg_...` 从 Prometheus 或自动化脚本调用 `/api/v1/metrics/prometheus` 等接口。更多场景写法见 [examples](examples) 目录。

### 多机分布式压测（零信任 mTLS）

在控制器节点启用集群监听端口，并在 Web 控制台「发压节点」页面签发加入令牌（或通过 `--cluster-token` 指定静态令牌）：

```bash
./tidegate serve --listen 0.0.0.0:8080 --cluster-listen 0.0.0.0:7443 --tls-self-signed
```

在其他发压机上携带加入令牌、CA 指纹与节点标签首次注册接入集群（后续重启仅凭 `--cert-dir` 内持久化的签名证书即可免令牌重连）：

```bash
./tidegate agent --join <控制器IP:7443> --token <加入令牌> --cert-dir ./agent-certs --ca-fingerprint <CA_SHA256指纹> --labels zone=cn-east,tier=perf
```

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
