export type Protocol = "openai" | "responses" | "anthropic" | "adapter";
export type ProviderKind = "api" | "subscription" | "local";
export type AuthMode = "api_key" | "oauth" | "none";
export type AccessScope = "lan" | "public" | "both";
export type RequestOutcome = "success" | "error" | "cancelled" | "incomplete";

export interface Provider {
  id: string;
  name: string;
  kind: ProviderKind;
  auth_mode: AuthMode;
  protocol: Protocol;
  endpoint: string;
  enabled: boolean;
  oauth_provider: string;
  has_secret: boolean;
  created_at: string;
}

export interface ProviderInput {
  id?: string;
  name: string;
  kind: ProviderKind;
  auth_mode: AuthMode;
  protocol: Protocol;
  endpoint: string;
  enabled: boolean;
  oauth_provider: string;
  secret?: string;
  remove_secret?: boolean;
}

export interface ProviderModelRef {
  id: string;
  input_images?: boolean;
}

export interface ProviderDiscovery {
  provider_id: string;
  status: "ok" | "unsupported" | "error" | string;
  models: ProviderModelRef[];
  message: string;
  checked_at: string;
  upstream_status: number;
  provider_enabled: boolean;
}

export interface ProviderTest {
  provider_id: string;
  model: string;
  protocol: Exclude<Protocol, "adapter"> | string;
  status: "ok" | "error" | string;
  message: string;
  checked_at: string;
  latency_ms: number;
  upstream_status: number;
  provider_enabled: boolean;
}

export interface Model {
  id: string;
  provider_id: string;
  upstream_model: string;
  name: string;
  protocols: Array<Exclude<Protocol, "adapter">>;
  input_images: boolean;
  enabled: boolean;
  input_price: number | null;
  output_price: number | null;
}

export interface ModelInput {
  id?: string;
  provider_id: string;
  upstream_model: string;
  name: string;
  protocols: Array<Exclude<Protocol, "adapter">>;
  input_images: boolean;
  enabled: boolean;
  input_price: number | null;
  output_price: number | null;
}

export interface Caller {
  id: string;
  name: string;
  allowed_models: string[];
  access_scope: AccessScope;
  local_only: boolean;
  enabled: boolean;
  rpm: number;
  max_concurrency: number;
  daily_token_limit: number;
  created_at: string;
}

export interface CallerCreateInput {
  id?: string;
  name: string;
  allowed_models: string[];
  access_scope: AccessScope;
  local_only: boolean;
  enabled: boolean;
  rpm: number;
  max_concurrency: number;
  daily_token_limit: number;
}

export interface CallerUpdateInput {
  name?: string;
  allowed_models?: string[];
  access_scope?: AccessScope;
  local_only?: boolean;
  enabled?: boolean;
  rpm?: number;
  max_concurrency?: number;
  daily_token_limit?: number;
}

export interface RequestRecord {
  id: string;
  caller_id: string;
  entry: string;
  model: string;
  provider_id: string;
  upstream_model: string;
  response_model: string;
  protocol: Protocol;
  status: number;
  error_code: string;
  started_at: string;
  duration_ms: number;
  ttft_ms: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  cache_tokens: number | null;
  reasoning_tokens: number | null;
  estimated_cost: number | null;
  attempts: number;
  outcome: RequestOutcome;
}

export interface RequestFilter {
  caller_id?: string;
  model?: string;
  status?: number;
  limit?: number;
}

export interface Summary {
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  unknown_usage_requests: number;
  estimated_cost: number | null;
  providers: number;
  ready_providers: number;
}

export interface Settings {
  adapter_configured: boolean;
  lan_base_url?: string;
  public_base_url?: string;
  supported_oauth?: string[];
}

export interface CallerCredential {
  caller: Caller;
  key: string;
}

export interface CallerKeyReveal {
  caller_id: string;
  available: boolean;
  key?: string;
  reason?: "legacy_key_not_saved";
}

export interface CallerKeySaveResult {
  caller_id: string;
  available: true;
}

export interface OAuthStart {
  provider: string;
  state: string;
  url: string;
  user_code?: string;
  expires_in?: number;
}

export interface OAuthStatus {
  provider: string;
  state: string;
  used: boolean;
  status?: string;
  error?: string;
  message?: string;
  completed?: boolean;
}

export type OAuthProvider = "codex" | "kimi";
export type OAuthAccountStatus = "missing" | "active" | "disabled" | "expired" | "error" | "unknown";

export interface OAuthAccount {
  provider: OAuthProvider;
  status: OAuthAccountStatus;
  account_count: number;
  model_prefix: string;
  disconnected?: boolean;
}

export interface OAuthCallbackInput {
  provider: string;
  state: string;
  code?: string;
  error?: string;
  callback_url?: string;
}

interface ItemResponse<T> {
  items: T[];
}

export class AdminApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "AdminApiError";
    this.status = status;
  }
}

export interface AdminClient {
  overview(): Promise<Summary>;
  providers(): Promise<Provider[]>;
  models(): Promise<Model[]>;
  callers(): Promise<Caller[]>;
  requests(filter?: RequestFilter): Promise<RequestRecord[]>;
  settings(): Promise<Settings>;
  discoverProviderModels(id: string): Promise<ProviderDiscovery>;
  testProvider(id: string, input: { model: string; protocol?: Exclude<Protocol, "adapter"> }): Promise<ProviderTest>;
  saveProvider(id: string, input: ProviderInput): Promise<Provider>;
  saveModel(id: string, input: ModelInput): Promise<Model>;
  createCaller(input: CallerCreateInput): Promise<CallerCredential>;
  updateCaller(id: string, input: CallerUpdateInput): Promise<Caller>;
  deleteProvider(id: string): Promise<void>;
  deleteModel(id: string): Promise<void>;
  deleteCaller(id: string): Promise<void>;
  rotateCaller(id: string): Promise<CallerCredential>;
  revealCallerKey(id: string): Promise<CallerKeyReveal>;
  saveCallerKey(id: string, key: string): Promise<CallerKeySaveResult>;
  startOAuth(provider: string): Promise<OAuthStart>;
  oauthStatus(state: string): Promise<OAuthStatus>;
  oauthCallback(input: OAuthCallbackInput): Promise<{ provider: string; state: string; status: string }>;
  oauthAccount(provider: OAuthProvider): Promise<OAuthAccount>;
  refreshOAuthAccount(provider: OAuthProvider): Promise<OAuthAccount>;
  disconnectOAuthAccount(provider: OAuthProvider): Promise<OAuthAccount>;
}

const API_ROOT = "/admin/api";

function encodePathPart(value: string): string {
  return encodeURIComponent(value);
}

function oauthProviderPath(provider: OAuthProvider): string {
  if (provider !== "codex" && provider !== "kimi") throw new Error("OAuth 提供商不受支持");
  return encodePathPart(provider);
}

function genericErrorMessage(status: number): string {
  if (status === 401 || status === 403) return "管理员认证失败";
  if (status === 404) return "管理接口不存在";
  if (status === 409) return "管理操作被拒绝";
  if (status >= 500) return "管理服务暂不可用";
  return "管理请求失败";
}

function normalizeOAuthStatus(status: OAuthStatus): OAuthStatus {
  const raw = status.status?.toLowerCase();
  if (raw === "ok" || raw === "complete" || raw === "completed" || status.completed === true) {
    return { ...status, status: "completed", completed: true };
  }
  if (raw === "wait" || raw === "waiting" || raw === "pending") {
    return { ...status, status: "pending", completed: false };
  }
  return status;
}

function normalizeOAuthAccount(account: OAuthAccount): OAuthAccount {
  const validStatuses: OAuthAccountStatus[] = ["missing", "active", "disabled", "expired", "error", "unknown"];
  return {
    provider: account.provider === "kimi" ? "kimi" : "codex",
    status: validStatuses.includes(account.status) ? account.status : "unknown",
    account_count: Number.isInteger(account.account_count) && account.account_count >= 0 ? account.account_count : 0,
    model_prefix: typeof account.model_prefix === "string" ? account.model_prefix : "",
    ...(account.disconnected === true ? { disconnected: true } : {}),
  };
}

export function createClient(adminKey: string, fetcher: typeof fetch = fetch): AdminClient {
  if (adminKey.length === 0 || adminKey.trim() !== adminKey) {
    throw new Error("管理员Key不能为空");
  }
  const authorization = `Bearer ${adminKey}`;

  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    headers.set("Accept", "application/json");
    headers.set("Authorization", authorization);
    if (init.body !== undefined && init.body !== null) {
      headers.set("Content-Type", "application/json");
    }
    let response: Response;
    try {
      response = await fetcher(`${API_ROOT}${path}`, { ...init, headers });
    } catch {
      throw new AdminApiError(0, "管理服务暂不可用");
    }
    if (!response.ok) {
      throw new AdminApiError(response.status, genericErrorMessage(response.status));
    }
    if (response.status === 204) {
      return undefined as T;
    }
    try {
      return (await response.json()) as T;
    } catch {
      throw new AdminApiError(response.status, "管理响应格式无效");
    }
  }

  function jsonRequest<T>(path: string, method: string, body: unknown): Promise<T> {
    return request<T>(path, { method, body: JSON.stringify(body) });
  }

  return {
    overview: () => request<Summary>("/overview"),
    providers: async () => (await request<ItemResponse<Provider>>("/providers")).items,
    models: async () => (await request<ItemResponse<Model>>("/models")).items,
    callers: async () => (await request<ItemResponse<Caller>>("/callers")).items,
    requests: async (filter = {}) => {
      const query = new URLSearchParams();
      if (filter.caller_id) query.set("caller_id", filter.caller_id);
      if (filter.model) query.set("model", filter.model);
      if (filter.status !== undefined) query.set("status", String(filter.status));
      if (filter.limit !== undefined) query.set("limit", String(filter.limit));
      const suffix = query.toString() ? `/requests?${query.toString()}` : "/requests";
      return (await request<ItemResponse<RequestRecord>>(suffix)).items;
    },
    settings: () => request<Settings>("/settings"),
    discoverProviderModels: (id) => request<ProviderDiscovery>(`/providers/${encodePathPart(id)}/models`),
    testProvider: (id, input) => jsonRequest<ProviderTest>(`/providers/${encodePathPart(id)}/test`, "POST", input),
    saveProvider: (id, input) => jsonRequest<Provider>(`/providers/${encodePathPart(id)}`, "PUT", input),
    saveModel: (id, input) => jsonRequest<Model>(`/models/${encodePathPart(id)}`, "PUT", input),
    createCaller: (input) => jsonRequest<CallerCredential>("/callers", "POST", input),
    updateCaller: (id, input) => jsonRequest<Caller>(`/callers/${encodePathPart(id)}`, "PUT", input),
    deleteProvider: (id) => request<void>(`/providers/${encodePathPart(id)}`, { method: "DELETE" }),
    deleteModel: (id) => request<void>(`/models/${encodePathPart(id)}`, { method: "DELETE" }),
    deleteCaller: (id) => request<void>(`/callers/${encodePathPart(id)}`, { method: "DELETE" }),
    rotateCaller: (id) => request<CallerCredential>(`/callers/${encodePathPart(id)}/rotate`, { method: "POST" }),
    revealCallerKey: (id) => request<CallerKeyReveal>(`/callers/${encodePathPart(id)}/key`),
    saveCallerKey: (id, key) => jsonRequest<CallerKeySaveResult>(`/callers/${encodePathPart(id)}/key`, "POST", { key }),
    startOAuth: (provider) => request<OAuthStart>(`/oauth/${encodePathPart(provider)}/start`, { method: "POST" }),
    oauthStatus: async (state) => normalizeOAuthStatus(await request<OAuthStatus>(`/oauth/status?state=${encodeURIComponent(state)}`)),
    oauthCallback: (input) => jsonRequest<{ provider: string; state: string; status: string }>("/oauth/callback", "POST", input),
    oauthAccount: async (provider) => normalizeOAuthAccount(await request<OAuthAccount>(`/oauth/${oauthProviderPath(provider)}/account`)),
    refreshOAuthAccount: async (provider) => normalizeOAuthAccount(await request<OAuthAccount>(`/oauth/${oauthProviderPath(provider)}/refresh`, { method: "POST" })),
    disconnectOAuthAccount: async (provider) => normalizeOAuthAccount(await request<OAuthAccount>(`/oauth/${oauthProviderPath(provider)}/account`, { method: "DELETE" })),
  };
}
