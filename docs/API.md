# API contract

The public inference contract is intentionally small. A deployment chooses its
own LAN and public base URLs; the versioned path is /v1.

## Authentication

Inference calls use a caller credential:

~~~http
Authorization: Bearer <caller-key>
~~~

The Anthropic Messages adapter may also accept its protocol-specific key
header when the deployment enables it. If two authentication headers conflict,
the request is rejected. Administrator credentials are valid only on the
management listener and must never be used as an inference key.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | /v1/models | List models visible to this caller |
| POST | /v1/chat/completions | OpenAI Chat Completions |
| POST | /v1/responses | OpenAI Responses |
| POST | /v1/messages | Anthropic Messages |

Model IDs are explicit public IDs returned by /v1/models. Unknown models,
disabled models, models outside the caller allowlist, and unsupported
protocols fail with a stable redacted error.

## Behavior

- Streaming responses use server-sent events where the selected protocol
  requires them.
- Client cancellation is propagated to the upstream request.
- The gateway does not transparently retry an upstream POST.
- After the first streamed event, it never changes provider or model.
- Missing upstream usage remains null/unknown.
- Local-only callers cannot reach cloud providers.
- A provider quota error does not silently become a paid-provider request.

The gateway records request metadata such as caller, selected model, upstream,
protocol, status, latency, attempts, and nullable usage. It does not normally
store prompts, source code, response bodies, raw credentials, or raw upstream
error bodies.

## Management boundary

The management API is a separate protected listener. Its list responses
return secret-presence and status metadata rather than secret material.
Management clients must keep the administrator credential in memory and must
not place it in a URL, browser storage, or a public inference request.
