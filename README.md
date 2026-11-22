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
| 接口管理 | 项目与目录树、多环境、多级变量、鉴权、单次调试、提取器、断言 |
| 场景编排 | 步骤引用接口、思考时间、数据文件驱动、流程控制、多场景混合 |
| 压测配置 | 固定并发和固定速率两种模式，预热、爬坡、保持、下降的阶段编排，阈值 |
| 实时图表 | 吞吐量、延迟百分位、错误、并发、带宽，可按接口拆分 |
| 报告 | 历史记录、错误采样、HTML 报告、多次对比 |
| 发压机监控 | 带宽占用、CPU、内存、端口、TCP 重传，饱和时告警 |
| 分布式集群 | 节点加入、负载分配、同步启动、指标合并 |
| 导入导出 | OpenAPI、cURL、Postman、HAR 等格式，项目可整体打包 |
| 部署 | 原生可执行文件和 Docker 镜像，支持 Windows、Linux、macOS |

完整的功能设计见[产品规划与功能设计](docs/product-plan.md)。

## 当前进度

目前已交付 **单文件控制台（Web 界面 + 控制器 + 本机发压节点）**、**CLI 压测与离线 HTML 报告**、**分布式集群** 与 **Docker 容器部署**：

- **Web 控制台与接口工作台**：本地账户初始化（一次性初始化码 + Argon2id）与多设备会话管理；项目 / 环境 / 会话级临时变量池；目录树拖拽排序与两步安全导入导出（`.tidegate`、OpenAPI 3.x / Swagger 2.0、Postman Collection v2.1、HAR 1.2、cURL）；多标签请求调试、cURL 导入导出与脱敏、生效预览（继承请求头 / 鉴权 / 变量来源）、JSONPath 可视化拾取、五段耗时瀑布条与“一键发起单接口压测”。
- **场景编排与发压引擎**：多步骤顺序执行、思考时间、CSV 参数化、开压前带宽与集群容量预估；闭环并发（VUs）与开环速率（RPS）双模式、多阶段线性曲线编辑与动态调压；SLA 阈值配置与运行时违约自动中止；基于 `uPlot` 的实时多维图表（吞吐量、延迟分位、活跃连接、应用层 vs 物理网卡双层带宽与瓶颈区间标注）及多压测基线对比。
- **报告与分布式集群**：离线单文件 HTML 报告、CSV / JSON 与错误采样 HAR 导出；支持多机节点一键接入、跨节点 HDR 直方图与系统遥测合并。

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
./tidegate run examples/ping.yaml --out result.json --csv result.csv --html report.html
```

场景中配置的 `thresholds` 在压测结束或运行期违约校验失败时，`tidegate run` 将返回退出码 `3`，便于流水线直接作为性能门禁拦截回归。更多场景写法见 [examples](examples) 目录。

### 多机分布式压测

在控制器节点启用集群监听端口与认证令牌：

```bash
./tidegate serve --listen 0.0.0.0:8080 --cluster-listen 0.0.0.0:7443 --cluster-token <集群令牌> --tls-self-signed
```

在其他发压机上接入集群：

```bash
./tidegate agent --join <控制器IP:7443> --token <集群令牌>
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
