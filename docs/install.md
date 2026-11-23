# Tidegate 安装、环境变量与运维指南

> 相关文档：[返回 README](../README.md) · [完整使用教程](user-guide.md) · [产品架构白皮书](product-plan.md)

---

## 目录

1. [源码编译（单文件部署）](#1-源码编译单文件部署)
2. [Docker / Compose 部署](#2-docker--compose-部署)
3. [Nginx / Caddy 反向代理配置](#3-nginx--caddy-反向代理配置)
4. [管理员账号、密码同步与 2FA 恢复](#4-管理员账号密码同步与-2fa-恢复)
5. [全量环境变量与 CLI 参数速查](#5-全量环境变量与-cli-参数速查)

---

## 1. 源码编译（单文件部署）

环境要求：**Go 1.26+**、**Node.js 22+ / 24+**（仅构建前端静态资源时需要；产出后可复制到任意同架构机器单文件运行）。

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

直接启动本地工作台（默认开启自动启动本机发压节点与打开浏览器）：

```bash
./tidegate
```

---

## 2. Docker / Compose 部署

仓库内包含多阶段非 root 静态镜像 [Dockerfile](../deploy/Dockerfile) 与开箱即用编排文件 [docker-compose.yml](../deploy/docker-compose.yml)：

```bash
# 设置初始 / 声明式同步管理员密码（建议使用 .env 文件或密码文件 Secret）
export TIDEGATE_ADMIN_PASSWORD="ChangeMe-StrongPassword!"
docker compose -f deploy/docker-compose.yml up -d --build
```

Windows PowerShell 启动示例：

```powershell
$env:TIDEGATE_ADMIN_PASSWORD = "Tidegate#2026"
docker compose -f deploy/docker-compose.yml up -d --build
Remove-Item Env:TIDEGATE_ADMIN_PASSWORD
```

默认暴露端口：

- `8080/tcp`：Web 控制台、REST API、WebSocket 遥测与 MCP Streamable HTTP（`deploy/docker-compose.yml` 默认启用 `--tls-self-signed`，通过 `https://localhost:8080/` 访问）。
- `7443/tcp`：集群双向 mTLS gRPC 通信端口。
- `/data` 持久化卷：存放 SQLite 数据库（`tidegate.db`）、AES-256-GCM 主密钥（`master.key`）、项目 YAML 及自签名证书。

如需挂载外部 TLS 证书并绑定固定域名：

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

将 Tidegate 置于 Nginx、Caddy 或云负载均衡之后时，推荐配置：

1. 在 Tidegate 端启用 `serve` 并设置：
   - `TIDEGATE_PUBLIC_URL=https://tidegate.example.com`（自动加入可信 Origin 并在 `/api/v1/system/info` 反馈外网地址）
   - `TIDEGATE_TRUSTED_PROXIES=127.0.0.1/8,10.0.0.0/8`（或填代理网段 CIDR、`loopback`、`private`）
   - 若前端与反代之间走明文内网 HTTP，可加 `TIDEGATE_ALLOW_INSECURE_REMOTE=true`（反代对公网仍需保持 HTTPS）
2. 反代必需透传 `X-Forwarded-Proto`、`X-Forwarded-Host`、`X-Forwarded-For` 与 **WebSocket Upgrade** 头：

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

> 安全说明：只有来自 `TIDEGATE_TRUSTED_PROXIES` 可信网段的连接才会信任 `X-Forwarded-*` 头，且系统会从右向左剥离可信代理来还原真实客户端 IP（防客户端伪造请求头绕过限流与审计）。

---

## 4. 管理员账号、密码同步与 2FA 恢复

Tidegate 提供**启动声明式环境变量同步**与**命令行维护**两种改密方式，二者均与 SQLite 账户表一致持久化，重启后保持一致：

### 方式一：环境变量声明式创建与重启同步（Docker 推荐）

- 启动时设置 `TIDEGATE_ADMIN_PASSWORD`（或 `TIDEGATE_ADMIN_PASSWORD_FILE`）：
  - 若目标管理员（默认 `admin`，可通过 `TIDEGATE_ADMIN_USER` 指定）**不存在**：跳过一次性初始化流程，直接自动创建该管理员账号。
  - 若该账号**已存在**：比对环境变量与数据库密码哈希；若一致则保持现有会话不动（幂等）；若不同则立即**覆盖数据库密码并吊销所有旧会话与恢复令牌**。
- 修改密码后若希望恢复自主改密，务必取消或移除该环境变量——只要环境变量保留，每次重启都会确保数据库密码与环境变量一致。
- 如同时丢失验证器（2FA），可临时设置 `TIDEGATE_ADMIN_RESET_TOTP=true` 启动一次，清空该管理员的 TOTP 绑定与恢复码。

### 方式二：命令行重置 (`tidegate reset-password`)

适合本地二进制、宿主机或容器内一次性交互/脚本重置：

```bash
# 交互式输入新密码（从标准输入读取）
./tidegate reset-password --data ./data --user admin

# 非交互方式直接指定新密码并顺便清空 2FA 绑定
./tidegate reset-password --data ./data --user admin --password "NewStrongPassword!" --reset-totp

# 从独立文件读取新密码
./tidegate reset-password --data ./data --user admin --password-file ./new_admin.pw
```

> 容器内执行示例：`docker exec -i tidegate-controller /usr/local/bin/tidegate reset-password --data /data --user admin --password "NewPassword!"`（若容器同时挂载了 `TIDEGATE_ADMIN_PASSWORD` 环境变量，下次容器重建/重启时仍会按环境变量同步）。

---

## 5. 全量环境变量与 CLI 参数速查

### `tidegate serve` 参数

| 环境变量 | CLI 参数 | 默认值 | 说明 |
|---|---|---|---|
| `TIDEGATE_DATA` | `--data` | `./data` | 数据目录（YAML 项目、SQLite、`master.key`） |
| `TIDEGATE_LISTEN` | `--listen` | `127.0.0.1:8080` | Web UI / HTTP API / WebSocket / MCP 监听地址 |
| `TIDEGATE_CLUSTER_LISTEN` | `--cluster-listen` | `127.0.0.1:7443` | 集群节点 gRPC 双向 mTLS 监听地址 |
| `TIDEGATE_CLUSTER_PUBLIC_ADDR` | `--cluster-public-addr` | `""` | 对外公告集群节点接入地址（`host:port`），用于 `/api/v1/system/info` |
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
| `TIDEGATE_TOKEN` | `--token` | `""` | 加入令牌（首次注册换签证书时使用） |
| `TIDEGATE_NAME` | `--name` | 主机名 | 发压节点显示名称 |
| `TIDEGATE_WORK_DIR` | `--work-dir` | `""` | 临时运行文件目录 |
| `TIDEGATE_CERT_DIR` | `--cert-dir` | `""` | mTLS 节点证书持久化目录（启用后重启免令牌续连） |
| `TIDEGATE_CA_FINGERPRINT` | `--ca-fingerprint` | `""` | 控制器集群 CA SHA-256 指纹校验值 |
| `TIDEGATE_LABELS` | `--labels` | `""` | 节点调度标签（如 `zone=cn-east,tier=perf`） |
| `TIDEGATE_AGENT_INSECURE` | `--insecure` | `false` | 禁用集群 mTLS 明文连接（仅测试用） |
| `TIDEGATE_CLIENT` | `--client` | `fast` | 发压 HTTP 客户端实现：`fast` 或 `std` |
