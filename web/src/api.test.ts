import { describe, expect, it, vi } from "vitest";
import { AdminApiError, createClient } from "./api";

function response(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("admin API client", () => {
  it("uses an in-memory bearer header and relative admin routes", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ items: [] }));
    const client = createClient("secret-admin-key", fetcher);

    await client.providers();

    expect(fetcher).toHaveBeenCalledOnce();
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/providers");
    expect(init?.headers).toBeInstanceOf(Headers);
    const headers = init?.headers as Headers;
    expect(headers.get("Authorization")).toBe("Bearer secret-admin-key");
    expect(headers.get("Accept")).toBe("application/json");
    expect(String(url)).not.toContain("secret-admin-key");
  });

  it("serializes bounded filters and never places credentials in the URL", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ items: [] }));
    const client = createClient("admin-key", fetcher);

    await client.requests({ caller_id: "caller one", model: "model/one", status: 500, limit: 100 });

    const [url] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/requests?caller_id=caller+one&model=model%2Fone&status=500&limit=100");
    expect(String(url)).not.toContain("admin-key");
  });

  it("sends secret-bearing input only in a JSON body over the protected route", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ id: "provider-a" }));
    const client = createClient("admin-key", fetcher);

    await client.saveProvider("provider-a", {
      name: "Provider A",
      kind: "api",
      auth_mode: "api_key",
      protocol: "openai",
      endpoint: "https://provider.example/v1",
      enabled: true,
      oauth_provider: "",
      secret: "upstream-secret",
    });

    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/providers/provider-a");
    expect(init?.method).toBe("PUT");
    expect(init?.body).toContain('"secret":"upstream-secret"');
    expect(String(url)).not.toContain("upstream-secret");
  });

  it("returns generic bounded errors without reading raw response bodies", async () => {
    const rawSensitiveBody = vi.fn().mockRejectedValue(new Error("body must not be read"));
    const failure = new Response(null, { status: 401 });
    Object.defineProperty(failure, "json", { value: rawSensitiveBody });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(failure);
    const client = createClient("admin-key", fetcher);

    await expect(client.settings()).rejects.toMatchObject({
      name: "AdminApiError",
      status: 401,
      message: "管理员认证失败",
    });
    expect(rawSensitiveBody).not.toHaveBeenCalled();
  });

  it("rejects an empty administrator key before making a request", () => {
    expect(() => createClient("")).toThrow("管理员Key不能为空");
    expect(() => createClient(" admin-key")).toThrow("管理员Key不能为空");
  });

  it("handles no-content deletes without parsing a body", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response(undefined, 204));
    const client = createClient("admin-key", fetcher);

    await expect(client.deleteCaller("caller-a")).resolves.toBeUndefined();
    expect(fetcher.mock.calls[0][1]?.method).toBe("DELETE");
  });

  it("reveals a caller key through the encoded protected route", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ caller_id: "company/laptop", available: true, key: "caller-key" }));
    const client = createClient("admin-key", fetcher);

    await expect(client.revealCallerKey("company/laptop")).resolves.toEqual({ caller_id: "company/laptop", available: true, key: "caller-key" });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/callers/company%2Flaptop/key");
    expect(init?.method).toBeUndefined();
    expect(init?.body).toBeUndefined();
    expect(String(url)).not.toContain("caller-key");
  });

  it("saves a legacy caller key only in the JSON body", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ caller_id: "company/laptop", available: true }));
    const client = createClient("admin-key", fetcher);

    await expect(client.saveCallerKey("company/laptop", "legacy-key")).resolves.toEqual({ caller_id: "company/laptop", available: true });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/callers/company%2Flaptop/key");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe(JSON.stringify({ key: "legacy-key" }));
    expect(String(url)).not.toContain("legacy-key");
  });

  it("redacts caller-key save errors without reading sensitive response content", async () => {
    const rawSensitiveBody = vi.fn().mockRejectedValue(new Error("body must not be read"));
    const failure = new Response(null, { status: 409 });
    Object.defineProperty(failure, "json", { value: rawSensitiveBody });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(failure);
    const client = createClient("admin-key", fetcher);

    await expect(client.saveCallerKey("company/laptop", "wrong-key")).rejects.toMatchObject({
      name: "AdminApiError",
      status: 409,
      message: "管理操作被拒绝",
    });
    expect(rawSensitiveBody).not.toHaveBeenCalled();
  });

  it("exposes typed API errors without exposing response content", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ secret: "do-not-return" }, 500));
    const client = createClient("admin-key", fetcher);

    await expect(client.overview()).rejects.toBeInstanceOf(AdminApiError);
    await expect(client.overview()).rejects.not.toThrow("do-not-return");
  });

  it("converts network failures into a bounded generic error", async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error("socket details"));
    const client = createClient("admin-key", fetcher);

    await expect(client.settings()).rejects.toMatchObject({
      status: 0,
      message: "管理服务暂不可用",
    });
  });

  it("uses fixed OAuth account routes and normalizes account state", async () => {
    const account = { provider: "codex", status: "unexpected", account_count: -1, model_prefix: "nr-codex/", token: "must-not-be-used" };
    const fetcher = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(response(account))
      .mockResolvedValueOnce(response({ provider: "codex", status: "active", account_count: 1, model_prefix: "nr-codex/" }))
      .mockResolvedValueOnce(response({ provider: "codex", status: "missing", account_count: 0, model_prefix: "nr-codex/", disconnected: true }));
    const client = createClient("admin-key", fetcher);

    await expect(client.oauthAccount("codex")).resolves.toMatchObject({ status: "unknown", account_count: 0, model_prefix: "nr-codex/" });
    await expect(client.refreshOAuthAccount("codex")).resolves.toMatchObject({ status: "active", account_count: 1 });
    await expect(client.disconnectOAuthAccount("codex")).resolves.toMatchObject({ status: "missing", account_count: 0, disconnected: true });

    expect(fetcher.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
      ["/admin/api/oauth/codex/account", undefined],
      ["/admin/api/oauth/codex/refresh", "POST"],
      ["/admin/api/oauth/codex/account", "DELETE"],
    ]);
    expect(JSON.stringify(fetcher.mock.calls)).not.toContain("must-not-be-used");
  });

  it("discovers provider models and URL-encodes provider IDs", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({
      provider_id: "bailian/qwen",
      status: "ok",
      models: [{ id: "qwen3.8-max" }],
      message: "模型发现成功",
      checked_at: "2026-09-20T00:00:00Z",
      upstream_status: 200,
      provider_enabled: false,
    }));
    const client = createClient("admin-key", fetcher);

    await expect(client.discoverProviderModels("bailian/qwen")).resolves.toMatchObject({ status: "ok" });
    expect(fetcher.mock.calls[0][0]).toBe("/admin/api/providers/bailian%2Fqwen/models");
  });

  it("posts an explicit provider test without putting the model in the URL", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({
      provider_id: "bailian",
      model: "qwen3.8-max",
      protocol: "openai",
      status: "error",
      message: "上游权限不足",
      checked_at: "2026-09-20T00:00:00Z",
      latency_ms: 31,
      upstream_status: 403,
      provider_enabled: false,
    }));
    const client = createClient("admin-key", fetcher);

    await expect(client.testProvider("bailian", { model: "qwen3.8-max", protocol: "openai" })).resolves.toMatchObject({ status: "error" });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/admin/api/providers/bailian/test");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe(JSON.stringify({ model: "qwen3.8-max", protocol: "openai" }));
    expect(String(url)).not.toContain("qwen3.8-max");
  });

  it("rejects unsupported OAuth account providers before network access", async () => {
    const fetcher = vi.fn<typeof fetch>();
    const client = createClient("admin-key", fetcher);

    await expect(client.oauthAccount("other" as "codex")).rejects.toThrow("OAuth 提供商不受支持");
    expect(fetcher).not.toHaveBeenCalled();
  });
});
