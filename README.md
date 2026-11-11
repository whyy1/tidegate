<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-wordmark-dark.svg">
    <img src="assets/logo-wordmark.svg" alt="Tidegate" height="72">
  </picture>
</p>

<p align="center">本地优先的 API 管理与压测一体化工具</p>

---

Tidegate 想把接口管理和压测放进同一个工具：在一个界面里管理接口、调试接口、编排场景，然后直接发起单机或多机压测，同时看到实时图表和发压机自身的带宽占用。

数据都在你自己的机器上，不需要云端账号，不加载任何远程代码。

## 现在可以用的

目前提供命令行压测，界面还在开发中。

- **声明式场景**：用 YAML 或 JSON 描述请求、变量、数据文件、提取器、断言和思考时间。
- **两种负载模式**：固定并发（闭环）和固定速率（开环）。开环模式的延迟从计划发送时间算起，被测服务变慢时延迟不会被低估。
- **阶段编排**：预热、爬坡、保持、下降可以自由组合。
- **每秒指标**：吞吐量、P50/P90/P95/P99、错误率和错误分类、上行和下行流量。发压机自身的错误（如端口耗尽）单独归类，不和被测服务的错误混在一起。
- **结果导出**：JSON（每秒快照和汇总）、CSV（每秒指标）。
- **支持 HTTP/1.1 和 HTTPS**，可在 Windows、Linux、macOS 上构建。

## 快速开始

需要 Go 1.25 或更高版本。

```bash
git clone https://github.com/whyy1/tidegate.git
cd tidegate
go build -o tidegate ./cmd/tidegate
```

Windows 上把输出文件名写成 `tidegate.exe`。

启动内置的被测服务，然后在另一个终端运行示例场景：

```bash
./tidegate testserver
./tidegate run examples/ping.yaml --out result.json --csv result.csv
```

运行时每秒输出一行摘要，结束后输出汇总。输出形如：

```
   时间   并发   请求/秒      P50      P95      P99  错误率        上行        下行
     1s    300   240,802   1.23ms   1.86ms   2.29ms   0.52%  180.1 Mbps  130.3 Mbps
     2s    300   262,391   1.13ms   1.76ms   2.18ms   0.00%  197.3 Mbps  142.7 Mbps
```

## 场景文件

```yaml
version: 1
name: 登录后查询
variables:
  host: http://127.0.0.1:18080
data:
  - name: users
    file: users.csv
    mode: sequential
steps:
  - id: login
    request:
      method: POST
      url: "{{host}}/login"
      body:
        type: json
        content: '{"user":"{{users.name}}","pwd":"{{users.pwd}}"}'
    extract:
      - name: token
        from: jsonpath
        expr: $.data.token
  - id: query
    request:
      method: GET
      url: "{{host}}/items"
      headers:
        Authorization: "Bearer {{token}}"
    assert:
      - type: status
        equals: 200
load:
  mode: vus
  stages:
    - duration: 3s
      target: 20
    - duration: 10s
      target: 20
```

完整的例子在 [examples](examples) 目录。

## 命令

| 命令 | 说明 |
|---|---|
| `tidegate run <场景文件>` | 运行压测场景。`--out` 写出 JSON，`--csv` 写出 CSV，`--quiet` 只输出汇总 |
| `tidegate testserver [--addr :18080]` | 启动内置的被测服务，用于自测 |
| `tidegate version` | 显示版本 |

## 性能

在一台 32 线程的 Windows 机器上压本机的 `/ping` 接口，发压程序和被测服务共用这台机器：

| 并发 | 吞吐量 | P99 延迟 | 发压进程内存 |
|---:|---:|---:|---:|
| 100 | 每秒 26.8 万请求 | 0.7 毫秒 | 42 MB |
| 1000 | 每秒 22.3 万请求 | 13 毫秒 | 65 MB |

可以用 `bash bench/run.sh` 在自己的机器上重跑。

## 规划中

- 浏览器界面：接口管理、单次调试、压测配置、实时图表、历史记录
- 发压机带宽和健康监控，发压机成为瓶颈时给出告警
- 多机集群合并发压
- 本地账户登录
- OpenAPI、Postman、HAR、cURL、JMeter 等格式的导入导出
- Docker 镜像

详细设计见[产品规划与功能设计](docs/product-plan.md)。

## 许可

Copyright (c) 2026 whyy1

源码公开，但不是开源软件。本项目采用 [PolyForm Strict License 1.0.0](LICENSE)：

- 允许个人学习、研究和其他非商业用途的使用。
- 不允许商业使用，不允许分发，不允许修改后再发布。
- 需要商用或分发授权，请在 [Issues](https://github.com/whyy1/tidegate/issues) 里联系作者。

以 [LICENSE](LICENSE) 的英文原文为准。
