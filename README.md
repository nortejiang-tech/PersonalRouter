# PersonalRouter

[中文](#中文) · [English](#english)

## 中文

### 1. 产品定位与范围

PersonalRouter 是一个**自托管的个人模型聚合网关**。它把多个上游供应商和本地模型服务聚合到统一的、需要调用方鉴权的入口之下，同时对外提供三种请求契约：

| 契约 | 端点 |
| --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |

核心特性：

- **模型必须明确选择**：调用方在请求里带上模型 ID，网关要么精确提供该模型，要么直接拒绝，不会替你挑选“差不多的”模型。
- **调用方 Key**：每个工具/Agent 使用自己的调用方 Key，网关按调用方身份鉴权与记账。
- **模型白名单**：每个调用方只能看到并使用被明确授权的模型；全局启用的模型若不在该调用方白名单中，同样不可请求。
- **用量记录**：每次请求留下使用记录，但**不包含**提示词正文、源代码、响应内容、凭据和上游原始错误。
- **流式转发**：支持 SSE 流式响应，流有边界且可取消。
- **取消**：客户端断开连接会停止上游请求。
- **脱敏错误**：返回给调用方的错误经过脱敏，不泄露上游凭据或内部细节。

### 2. 公开版边界

本仓库是**脱敏的 public snapshot（公开快照）**：

- 私有仓库是**唯一的规范维护源（canonical maintenance source）**，公开版只在维护者明确要求时同步。
- 公开版**不包含**真实的供应商订阅登录、供应商 API Key、调用方 Key、生产主机、域名、Tunnel/隧道工具、备份以及内部运维与评审资料。
- 公开版**不会替任何用户执行登录或 API 配置**，也没有连接到任何人的真实基础设施。

### 3. 架构与监听面

技术栈：**Go 服务 + SQLite 状态 + React/TypeScript 管理界面**。

```
                      ┌──────────────────────────────────────────┐
 调用方工具 ─────────►│  局域网推理入口 lan_listen               │
                      │  （loopback 或私网 IP）                  │
                      ├──────────────────────────────────────────┤
 TLS 反向代理 ───────►│  公网推理入口 public_listen              │
 （由你自行管理）      │  只能绑定 loopback                       │
                      ├──────────────────────────────────────────┤
 运维浏览器 ────────►│  管理入口 admin_listen                   │
                      │  绝不公网暴露                            │
                      └──────────────────────────────────────────┘
                                     │
                  SQLite 状态 + 仅属主可读的密钥文件（data_dir）
                                     │
              上游供应商  /  可选的外部 adapter
```

- `lan_listen`：**局域网推理入口**，可绑定 loopback 或私网 IP。
- `public_listen`：**公网推理入口**，**只能绑定 loopback**，远端访问必须经由你自己管理的 TLS 反向代理或隧道转发进来。
- `admin_listen`：**管理入口**（管理 UI 与 `/admin/api`），**任何情况下都不能公网暴露**。
- `data_dir`：保存 `master.key`（静态加密供应商/adapter 密钥）、`admin.key`（管理密钥）、`personalrouter.db`（SQLite 状态），必须**仅属主可访问**，且 `web_dir` 不得位于 `data_dir` 内部。
- 凭据**不进 URL**：配置校验会拒绝带 userinfo、query 或 fragment 的 URL。

### 4. 部署前提与步骤

**前提**

- **Go 1.25 或更新版本**。
- 与仓库内 web lockfile 兼容的 **Node.js/npm**。
- 一个**仅属主可写的 data 目录**（将存放 `master.key`、`admin.key`、`personalrouter.db`）。
- 一个**独立的、绝对路径的 web 构建目录**（不得在 `data_dir` 内）。

**构建**

```bash
npm --prefix web ci
npm --prefix web run build
go build -o ./bin/personalrouter ./cmd/personalrouter
```

**配置示例**（全部为占位符）

```json
{
  "data_dir": "/absolute/path/to/personalrouter-data",
  "lan_listen": "<lan-private-ip>:8787",
  "admin_listen": "127.0.0.1:8788",
  "public_listen": "127.0.0.1:8790",
  "web_dir": "/absolute/path/to/personalrouter-web",
  "lan_base_url": "http://<lan-private-ip>:8787/v1",
  "public_base_url": "https://<public-proxy-host>/v1",
  "adapter_url": "<adapter-url>",
  "adapter_management_key_file": "/absolute/path/to/adapter-management.key",
  "adapter_api_key_file": "/absolute/path/to/adapter-api.key"
}
```

| 字段 | 是否必需 | 说明 |
| --- | --- | --- |
| `data_dir` | 必需 | 绝对路径，仅属主；存放密钥与 SQLite 数据库。 |
| `lan_listen` | 必需（默认 `127.0.0.1:8787`） | 明确 IP + 端口；仅 loopback 或私网 IP。 |
| `admin_listen` | 必需（默认 `127.0.0.1:8788`） | 明确 IP + 端口；绝不公网暴露。 |
| `public_listen` | 必需（默认 `127.0.0.1:8790`） | 明确 IP + 端口；**只能 loopback**。 |
| `web_dir` | 使用 UI 时必需 | 绝对路径的构建产物；不得在 `data_dir` 内。 |
| `lan_base_url` | 展示/URL 值 | 局域网内调用方应使用的 base URL。 |
| `public_base_url` | 展示/URL 值 | 代理之后的公网 HTTPS base URL。 |
| `adapter_url` | 可选 | 外部 adapter 的 HTTP(S) 地址；不得含 userinfo、query、fragment。 |
| `adapter_management_key_file` | 可选 | 绝对路径，指向仅属主的密钥文件。 |
| `adapter_api_key_file` | 可选 | 绝对路径，指向仅属主的密钥文件。 |

配置约束：

- 所有路径**必须是绝对路径**。
- 监听地址**必须是明确的 IP + 端口**，主机名、通配地址（如 `0.0.0.0`）与 DNS 名称都会被拒绝。
- 三个监听地址**互不相同**，各自使用独立端口。
- `public_listen` **只能绑定 loopback**；`lan_listen` 可用 loopback 或私网 IP。
- `adapter_url`、`adapter_management_key_file`、`adapter_api_key_file` 只有在确实部署外部 adapter 时才需要，否则省略。

**启动**

```bash
./bin/personalrouter -config /absolute/path/to/personalrouter.json
```

`-config` 要求指向一个非机密 JSON 文件的**绝对路径**：配置文件本身只含路径与地址，不含凭据。

**首次启动**：网关会创建 data 目录并在其中生成 `master.key`、`admin.key`、`personalrouter.db`，密钥文件权限为仅属主。**管理员 Key 是管理机密**，请用你本地受控的方式（密码管理器、加密文件、密钥 CLI 等）取回并保管，**不得提交到 Git、贴到工单或聊天、打进日志或留在 shell 历史**。持有管理员 Key 的人可以添加供应商、签发调用方 Key 并访问管理面。

### 5. 管理和接入顺序

在浏览器打开 `admin_listen` 地址上的管理 UI（例如 `http://127.0.0.1:8788/`），使用管理员 Key 登录。

| 路径 | 用途 |
| --- | --- |
| `/` | 管理 UI（来自 `web_dir` 的静态构建）。 |
| `/healthz` | 存活探针。 |
| `/admin/api/...` | 受保护的管理 API（供应商、模型、调用方、密钥、用量、adapter 状态）。 |

推荐接入顺序：

1. **添加供应商**：直连官方端点，或使用该供应商所需的 adapter。
2. **选择认证方式**：由供应商/adapter 支持情况决定，使用**订阅登录**或 **API Key**；并非每个供应商两者都支持。
3. **联通测试与模型发现**：发现会返回精确的上游模型 ID，**客户端必须使用发现返回的 ID**，不要手写或猜测。
4. **启用模型并设置调用方白名单**：让每个调用方只看到它应用的模型。
5. **创建调用方并签发可复用的调用方 Key**：每个需要共享身份的工具/Agent 使用一个调用方 Key。

模型页表单行为：新模型的公开 ID 按 `上游 ID/上游模型 ID` 自动生成，可在高级区修改；保存新模型后表单回到同一上游的全新新增态，便于连续添加多个模型；在目录中选择**已经配置过**的上游模型时，表单会载入那条既有记录进入编辑（不会重复建模），如需新建不同公开 ID 请先改用未配置过的上游模型。

> **公开版不会替用户执行登录或 API 配置**：不附带任何供应商凭据、订阅会话或调用方 Key。请先查阅各供应商**官方文档**了解当前的认证流程、端点地址与模型命名；这些内容会变化，本仓库不做镜像。协议相关的预置见 `docs/PROVIDER-PRESETS.md`。

### 6. 调用示例

以下示例全部使用占位符：`<caller-key>` 换成你在管理 UI 中签发的调用方 Key，主机换成你的局域网或公网推理地址。

```bash
# 列出该调用方可用的模型
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  http://<lan-private-ip>:8787/v1/models

# OpenAI Chat Completions
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "<model-id>", "messages": [{"role": "user", "content": "Hello"}], "stream": false}' \
  http://<lan-private-ip>:8787/v1/chat/completions

# OpenAI Responses
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "<model-id>", "input": "Hello", "stream": false}' \
  http://<lan-private-ip>:8787/v1/responses

# Anthropic Messages
curl -sS \
  -H "x-api-key: <caller-key>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model": "<model-id>", "max_tokens": 256, "messages": [{"role": "user", "content": "Hello"}], "stream": false}' \
  http://<lan-private-ip>:8787/v1/messages
```

通用规则：

- 认证使用 `Authorization: Bearer <caller-key>`；Anthropic Messages 契约使用 `x-api-key` 加 `anthropic-version` 头。
- `base_url` **必须以 `/v1` 结尾**。
- **模型必须出现在该调用方的 `/v1/models` 里**，否则不可请求。
- **自动 fallback 默认关闭**：所选模型耗尽、限流或宕机时，网关不会静默切到别的模型或供应商。
- **流式是 SSE**：设 `"stream": true` 并按 `text/event-stream` 消费。
- **已经开始的流不会被不透明地重试或换路由**：一旦已向调用方发出字节，网关不会在另一条路由上重启请求；中途中断表现为流结束/失败，由调用方决定如何处理。

### 7. ZCode 等工具配置

任何可自定义 **Base URL** 与 **API Key** 的编码工具/Agent（ZCode 及同类工具是其中例子），都可以这样接入：

- 把工具的 **Base URL 指向局域网推理 URL 或公网推理 URL**（即 `lan_base_url` 或 `public_base_url`，以 `/v1` 结尾），并保证契约路径正确。
- 把**调用方 Key 填入工具的 API Key 字段**。
- 自己网络内的机器用 LAN URL；工具运行在别处时用公网 HTTPS URL。

各工具具体字段名称由该工具自身决定，PersonalRouter 只要求正确的 base URL、bearer 凭据与契约路径。本文件不提供、也不应出现任何真实主机、域名或地址。

### 8. 安全和排错

**安全**

- **凭据不进 Git、不进 URL、不进日志**：配置只放路径与地址，供应商密钥存在加密数据库里，请求记录不含提示词、源码、响应体、凭据与上游原始错误。
- **管理员 Key 与调用方 Key 严格分离**：管理员 Key 用于管理，调用方 Key 用于推理，绝不可互相替代。
- **管理面绝不公网暴露**：`admin_listen` 保持 loopback 或私网网段，绝不通过公网代理/隧道转发。
- `data_dir`、`master.key`、`admin.key` 与 adapter 密钥文件均为仅属主（目录 `0700`、文件 `0600`）；权限不对或是符号链接的密钥文件会被拒绝。
- **备份整个 `data_dir` 并加密**，存到工作树之外。`master.key`、`admin.key`、`personalrouter.db` **三份文件必须作为一个整体一起备份、一起恢复**：只备份数据库和 `master.key` 无法保留管理员凭据，漏掉 `admin.key` 就等于丢失管理员 Key，部分备份不是备份。
- 设备丢失、工具下线或疑似泄露时**轮换/吊销调用方 Key**；调用方白名单尽量收窄。

**常见错误与处理方向**

| 现象 | 可能原因与处理 |
| --- | --- |
| `config path must be absolute` / 配置不可用 | `-config` 传了相对路径或文件不可读；改用绝对路径。 |
| `config invalid` | JSON 格式错误或存在**未知字段**（未知字段会被拒绝）；修正字段名。 |
| `must contain an explicit IP and port` | 监听地址缺少主机或端口。 |
| `hostname, wildcard, or DNS address is not allowed` | 使用了主机名或通配地址；改用明确的 loopback 或私网 IP。 |
| `public listener must be loopback` | `public_listen` 设成了非 loopback IP；改为 loopback 并在前面加代理。 |
| `listener must be loopback or private` | `lan_listen`/`admin_listen` 用了非私网的公网 IP。 |
| `listener addresses must be distinct` | 两个监听地址共用同一 host:port；各自分配独立端口。 |
| `data_dir must be absolute` / `web_dir must be absolute` / `secret file path must be absolute` | 配置里用了相对路径；改为绝对路径。 |
| `401 Unauthorized` | 调用方/管理员 Key 缺失、错误或已吊销。 |
| `403 Forbidden` | 已认证但无权限：凭据类型与监听面不匹配、调用方未获该模型授权，或仅限局域网的调用方从别处接入。 |
| `model not available for this caller` | 该模型 ID 不在该调用方白名单中（或被禁用）；查看该调用方的 `/v1/models`，并核对发现返回的精确 ID。 |
| `web unavailable` | `web_dir` 缺失、未构建或不可读；执行 web 构建并把 `web_dir` 指向构建产物。注意 UI 只由管理入口提供。 |
| 供应商发现/联通失败 | 端点错误、未认证、adapter 未运行或上游故障；在管理 UI 重跑供应商测试。 |
| 上游配额/限流 | 上游返回限流错误，对调用方已脱敏且**不会**换模型重试；检查配额并降低请求速率。 |
| 流中途断开 | 上游断连；网关不会为已开始的流换路由，调用方应从头重试。 |

**本快照的限制**：本 README 与链接文档未描述的行为，按“未记录”处理而不作猜测。具体而言，供应商订阅登录流程、生产部署/隧道辅助、备份与数据库恢复工具、以及环境相关的运维手册都在私有维护仓库中，**不在**本公开快照内。

### 9. 文档入口

| 文档 | 内容 |
| --- | --- |
| [`docs/API.md`](docs/API.md) | 推理与管理 API 参考。 |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | 监听面、状态模型、请求生命周期。 |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | 构建、测试与贡献流程。 |
| [`docs/PROVIDER-PRESETS.md`](docs/PROVIDER-PRESETS.md) | 协议感知的供应商预置。 |
| [`docs/SECURITY.md`](docs/SECURITY.md) | 威胁模型、密钥处理、监听策略。 |
| [`PUBLIC-MIRROR.md`](PUBLIC-MIRROR.md) | 本脱敏镜像是什么、不是什么。 |

## English

PersonalRouter is a **self-hosted personal model aggregation gateway**. One
caller-authenticated base URL fronts several upstream model providers and local
model services, speaking three request contracts at once:

| Contract | Endpoint |
| --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |

Model selection is always **explicit**: a caller sends a model ID and the gateway
either serves that exact model or rejects the request. Callers are authenticated
with their own keys, restricted to an allowlist of models, can be confined to
LAN-only access, and every request leaves a usage record that excludes prompt text,
source code, response bodies, credentials, and raw upstream errors. Streaming is
bounded and cancellable, and errors returned to callers are redacted.

> **About this repository.** This is a **sanitized public snapshot**. A separate
> private repository is the canonical maintenance source; the public copy is
> updated only when a maintainer explicitly asks for a new export. Provider
> subscription logins, provider API keys, production deployment and tunnel
> helpers, real hostnames and IP addresses, caller keys, backups, and internal
> review material are **intentionally absent** here. Nothing in this repository
> is wired to anybody's real infrastructure.

---

## 1. Architecture at a glance

```
                      ┌──────────────────────────────────────────┐
 caller tools ───────►│  LAN inference listener (loopback or     │
                      │  private IP)  lan_listen                 │
                      ├──────────────────────────────────────────┤
 TLS reverse proxy ──►│  Public inference listener               │
 (separately managed) │  public_listen  (loopback ONLY)          │
                      ├──────────────────────────────────────────┤
 operator browser ───►│  Admin / management listener             │
                      │  admin_listen  (never published)         │
                      └──────────────────────────────────────────┘
                                     │
                    SQLite state + owner-only key files (data_dir)
                                     │
              upstream providers  /  optional external adapter
```

- **Go service, SQLite-backed state.** Providers, models, callers, caller keys,
  allowlists, and usage records live in one SQLite database inside `data_dir`.
- **React + TypeScript management UI**, built separately and served by the
  **management listener** once `web_dir` points at the build output.
- **Three independently configured listeners.** `lan_listen` may bind a loopback
  or a private IP. `public_listen` **must** bind loopback — remote access is
  expected to arrive through a separately managed TLS reverse proxy or tunnel.
  `admin_listen` is the management surface and is **never** published.
- **Optional external adapter.** `adapter_url` plus two owner-only secret-file
  paths (`adapter_management_key_file`, `adapter_api_key_file`) support an
  external adapter service for providers that need one. The adapter itself is
  outside this snapshot; the gateway only knows how to talk to it.
- **Credentials never go in URLs.** Provider entries, adapter entries, and
  caller entries keep secrets out of the URL string; the config validator
  rejects URLs carrying userinfo, query strings, or fragments.

### The data directory

`data_dir` is **owner-only** and holds everything sensitive:

| File | Purpose |
| --- | --- |
| `master.key` | Generated. Encrypts stored provider/adapter secrets at rest. |
| `admin.key` | Generated. The management secret for `/admin/api` and the UI. |
| `personalrouter.db` | SQLite database with all gateway state. |

This directory must be **backed up and protected outside Git**. It must never be
committed, and `web_dir` must not be located inside `data_dir`.

---

## 2. Deployment

### 2.1 Requirements

- **Go 1.25 or newer.**
- **Node.js/npm** compatible with the checked-in web lockfile.
- An **owner-only writable data directory** (it will hold `master.key`,
  `admin.key`, and `personalrouter.db`).
- A **separate absolute web build directory** (must not be inside `data_dir`).

### 2.2 Build

```bash
npm --prefix web ci
npm --prefix web run build
go build -o ./bin/personalrouter ./cmd/personalrouter
```

### 2.3 Configuration

Every configured path **must be absolute**. Every listener address **must include
an explicit IP and port** — hostnames, wildcards (`0.0.0.0`), and DNS names are
rejected. All three listeners **must be distinct**. `lan_listen` may use
loopback or a private IP; `public_listen` must use loopback.

```json
{
  "data_dir": "/absolute/path/to/personalrouter-data",
  "lan_listen": "<lan-private-ip>:8787",
  "admin_listen": "127.0.0.1:8788",
  "public_listen": "127.0.0.1:8790",
  "web_dir": "/absolute/path/to/personalrouter-web",
  "lan_base_url": "http://<lan-private-ip>:8787/v1",
  "public_base_url": "https://<public-proxy-host>/v1",
  "adapter_url": "<adapter-url>",
  "adapter_management_key_file": "/absolute/path/to/adapter-management.key",
  "adapter_api_key_file": "/absolute/path/to/adapter-api.key"
}
```

| Key | Required | Notes |
| --- | --- | --- |
| `data_dir` | Yes | Absolute. Owner-only. Holds keys and the SQLite database. |
| `lan_listen` | Yes (default `127.0.0.1:8787`) | Explicit IP + port. Loopback **or** private IP only. |
| `admin_listen` | Yes (default `127.0.0.1:8788`) | Explicit IP + port. Loopback or private. Never public. |
| `public_listen` | Yes (default `127.0.0.1:8790`) | Explicit IP + port. **Loopback only.** |
| `web_dir` | For the UI | Absolute build output. Must not be inside `data_dir`. |
| `lan_base_url` | Display/URL value | The base URL callers on the LAN should use. |
| `public_base_url` | Display/URL value | The public HTTPS base URL behind your proxy. |
| `adapter_url` | Optional | HTTP(S) adapter endpoint. No userinfo, query, or fragment. |
| `adapter_management_key_file` | Optional | Absolute path to an owner-only secret file. |
| `adapter_api_key_file` | Optional | Absolute path to an owner-only secret file. |

Notes on the placeholders above: `<lan-private-ip>` is a private-range address on
your own network, `<public-proxy-host>` is the hostname your TLS proxy serves,
and `<adapter-url>` is the HTTP(S) endpoint of an adapter you run yourself.
`adapter_url`, `adapter_management_key_file`, and `adapter_api_key_file` are
only needed if you actually deploy an external adapter; omit them otherwise.

### 2.4 Start

```bash
./bin/personalrouter -config /absolute/path/to/personalrouter.json
```

The `-config` flag requires an **absolute** path to a non-secret JSON file. The
config file itself contains no credentials — only paths and addresses — which is
why it is safe to keep in a config-management system that is not a secret store.

### 2.5 First start

On first start the gateway creates the data directory and, inside it,
`master.key`, `admin.key`, and `personalrouter.db`. Key files are created with
owner-only permissions.

**The admin key is a management secret.** Retrieve it with your own local secret
procedure (a password manager, an encrypted file, a secrets CLI — whatever you
already use) and store it there. Do **not** commit it, paste it into tickets or
chat, echo it into logs, or leave it in shell history. Anyone holding the admin
key can add providers, mint caller keys, and read the management surface.

### 2.6 Public exposure boundary

The gateway deliberately refuses to bind the public listener to anything but
loopback. The supported pattern is:

```
internet ──TLS──► your reverse proxy / tunnel ──► 127.0.0.1:<public port>
```

Terminate TLS at the proxy or tunnel you manage yourself, then forward to the
loopback `public_listen` address. Configure `public_base_url` to the HTTPS URL
your proxy serves. The **admin listener stays LAN-only** and must not be routed
through that proxy under any circumstances. How you run the proxy or tunnel —
system service manager, reverse proxy software, or a tunnel daemon — is outside
this snapshot; no specific host, domain, or provider is prescribed.

---

## 3. Management and provider setup

Open the management UI at `http://<admin-listen-host>:<admin-port>/` (the
`admin_listen` address) and authenticate with the admin key. Useful surfaces:

| Path | Purpose |
| --- | --- |
| `/` | Management UI (static build from `web_dir`). |
| `/healthz` | Liveness probe. |
| `/admin/api/...` | Protected management API (providers, models, callers, keys, usage, adapter status). |

Typical setup order:

1. **Add a provider** — either an official provider endpoint configured
   directly, or the configured adapter if that provider needs one.
2. **Choose the auth mode** the provider/adapter supports: **subscription
   login** or **API key**. Not every provider supports both.
3. **Run connectivity check and model discovery.** Discovery returns the exact
   upstream model IDs. **The IDs discovery returns are the values your clients
   must use** — do not guess or hand-write them.
4. **Add and enable models**, then set **caller allowlists** so each caller sees
   only the models it should.
5. **Create a caller** and issue **one reusable caller key** per tool/agent that
   should share an identity.
6. **Use the caller key only on inference endpoints.** Caller keys are not admin
   credentials and admin keys are not caller keys.

> **This public snapshot performs nobody's login or API-key provisioning.** No
> provider credentials, subscription sessions, or caller keys ship with it.
> Check each provider's **official documentation** for current authentication
> flows, endpoint URLs, and model naming before configuring; those details
> change and are not mirrored here. See `docs/PROVIDER-PRESETS.md` for the
> protocol-aware presets this snapshot documents.

---

## 4. Calling the API

All examples use placeholders. Replace `<caller-key>` with a caller key you
issued in the management UI, and the host with your LAN or public inference
base.

### List the models this caller may use

```bash
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  http://<lan-private-ip>:8787/v1/models
```

### Chat Completions

```bash
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<model-id>",
    "messages": [{ "role": "user", "content": "Hello" }],
    "stream": false
  }' \
  http://<lan-private-ip>:8787/v1/chat/completions
```

### Responses

```bash
curl -sS \
  -H "Authorization: Bearer <caller-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<model-id>",
    "input": "Hello",
    "stream": false
  }' \
  http://<lan-private-ip>:8787/v1/responses
```

### Anthropic Messages

```bash
curl -sS \
  -H "x-api-key: <caller-key>" \
  -H "authorization: Bearer <caller-key>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<model-id>",
    "max_tokens": 256,
    "messages": [{ "role": "user", "content": "Hello" }],
    "stream": false
  }' \
  http://<lan-private-ip>:8787/v1/messages
```

### Rules that apply to every request

- **The model must appear in `/v1/models` for that caller.** A model that is
  not in the caller's allowlist is not requestable, even if it is enabled
  globally.
- **Automatic fallback is disabled by default.** The gateway does not silently
  switch to a different model or provider when the selected one is exhausted,
  rate-limited, or down.
- **Streaming uses SSE.** Set `"stream": true` and consume `text/event-stream`.
- **An in-flight stream is not transparently retried or rerouted.** Once bytes
  have been sent to the caller, the gateway will not restart the request on
  another route; a mid-stream interruption surfaces as an ended/failed stream
  and the caller decides what to do.
- **Cancellation is honoured.** Closing the client connection stops the
  upstream request.

---

## 5. Client configuration

Any OpenAI-compatible SDK works by pointing `base_url` at the inference base
path ending in `/v1`, with the caller key as the API key:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://<lan-private-ip>:8787/v1",   # or https://<public-proxy-host>/v1
    api_key="<caller-key>",
)

resp = client.chat.completions.create(
    model="<model-id>",                            # must be listed in /v1/models
    messages=[{"role": "user", "content": "Hello"}],
)
```

For **Anthropic-compatible clients**, point the client at the same host's
`/v1/messages` contract and supply the caller key in the Anthropic auth header
along with an `anthropic-version` header.

Coding agents and IDE/CLI tools that expose configurable **base URL** and **API
key** fields (ZCode and similar tools are examples) can be pointed at whichever
inference URL you choose — the LAN base URL or the public HTTPS base URL — using
the caller key as the API key. Choose the LAN URL for machines on your own
network and the public URL when the tool runs elsewhere. Which field names each
tool uses is that tool's concern; PersonalRouter only requires the base URL, the
bearer credential, and the correct contract path.

---

## 6. Security and operations

**Secrets**

- `data_dir`, `master.key`, `admin.key`, and the adapter secret files are
  **owner-only** (`0700` directories, `0600` files). The gateway refuses to use
  key files with wrong permissions or that are symlinks.
- **No credentials in Git, in URLs, or in logs.** Config holds paths and
  addresses only; provider secrets live in the encrypted database, and request
  records exclude prompt text, source code, response bodies, credentials, and
  raw upstream errors.
- **Admin credentials and inference credentials are separate.** An admin key
  manages the gateway; a caller key calls it. Never substitute one for the other.
- **Management is never public.** Keep `admin_listen` on loopback or a private
  network segment and never route it through the public proxy or tunnel.

**Operations**

- Use **`/healthz`** for liveness checks and monitoring.
- **Back up the entire `data_dir` encrypted** and store the backup outside the
  working tree. All three files — `master.key`, `admin.key`, and
  `personalrouter.db` — must be backed up and restored **together** as one
  set. The database plus `master.key` alone do **not** preserve the existing
  administrator credential: `admin.key` is a separate owner-only file, so a
  backup that omits it loses the admin credential and the restored gateway
  will not accept the admin key you were using. A partial backup is not a
  backup.
- **Rotate and revoke caller keys** when a device is lost, a tool is
  decommissioned, or a key may have leaked. Rotation is a management-API/UI
  operation; old keys stop working.
- Keep the caller allowlist as narrow as practical; add models to a caller only
  when a tool actually needs them.

**Common errors**

| Symptom | Likely cause and fix |
| --- | --- |
| `config path must be absolute` / configuration unavailable | `-config` got a relative path or unreadable file. Pass an absolute path. |
| `config invalid` | Malformed JSON or an **unknown key** (unknown fields are rejected). Fix the key names. |
| `must contain an explicit IP and port` | Listener lacks a port or host. |
| `hostname, wildcard, or DNS address is not allowed` | Listener used a name or `0.0.0.0`. Use an explicit loopback or private IP. |
| `public listener must be loopback` | `public_listen` was set to a non-loopback IP. Set it to loopback and front it with a proxy. |
| `listener must be loopback or private` | `lan_listen`/`admin_listen` used a non-private public IP. |
| `listener addresses must be distinct` | Two listeners share the same host:port. Give each one its own port. |
| `data_dir must be absolute` / `web_dir must be absolute` / `secret file path must be absolute` | Relative path in config. Use absolute paths. |
| `401 Unauthorized` | Missing, wrong, or revoked caller/admin key. |
| `403 Forbidden` | Authenticated but not permitted — wrong listener for the credential type, caller not allowed that model, or a LAN-only caller reaching in from elsewhere. |
| `model not available for this caller` | The model ID is not in that caller's allowlist (or is disabled). Check `/v1/models` for that caller and re-check the exact discovered ID. |
| `web unavailable` | `web_dir` missing, not built, or not readable. Run the web build and point `web_dir` at the build output. Note the UI is served only by the management listener. |
| Provider discovery / connectivity failure | Wrong endpoint, unauthenticated provider, adapter not running, or upstream outage. Re-run the provider test from the management UI. |
| Upstream quota / rate limit | Upstream returned a limit error. It is redacted to the caller and **not** retried onto another model — check quota and lower request rate. |
| Stream interrupted mid-response | Upstream dropped the connection. The gateway will not reroute a started stream; the caller should retry from scratch. |

**Limitations of this snapshot.** Behaviour not described in this README and the
linked docs is not documented here rather than guessed. Specifically: provider
subscription login flows, production deployment/tunnel helpers, backup and
database recovery tooling, and environment-specific operational runbooks are
part of the private maintenance repository and are **not** present in this
public snapshot.

---

## 7. Further reading

| Document | Contents |
| --- | --- |
| [`docs/API.md`](docs/API.md) | Inference and management API reference. |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Listeners, state model, request lifecycle. |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | Build, test, and contribution workflow. |
| [`docs/PROVIDER-PRESETS.md`](docs/PROVIDER-PRESETS.md) | Protocol-aware provider presets. |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, secret handling, listener policy. |
| [`PUBLIC-MIRROR.md`](PUBLIC-MIRROR.md) | What this sanitized mirror is and is not. |

## License

**This sanitized snapshot currently contains no license file.** Publishing it
publicly does not grant any right to reuse, copy, modify, or redistribute this
code or documentation. A license may be added later by an explicit maintainer
decision; until then, no license is offered. The private repository remains the
canonical source of truth.
