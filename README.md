# PersonalRouter

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
