/**
 * EVO-006 official website model candidate catalog.
 *
 * Pure display data + pure functions only.
 * - No network, no credentials, no runtime configuration.
 * - Results are deep-frozen so callers cannot pollute later results.
 * - "Official candidate" only: does NOT claim the account is authorized
 *   or that a model has been tested successfully.
 */

export interface OfficialModel {
  readonly id: string;
  readonly input_images: boolean;
}

export interface OfficialModelCatalog {
  readonly label: string;
  readonly source: string;
  readonly checkedAt: string;
  readonly models: readonly OfficialModel[];
}

const CHECKED_AT = "2026-09-20";

/** Normalized endpoint: lowercased host, path with at most one trailing slash removed. */
interface NormalizedEndpoint {
  host: string;
  path: string;
}

/**
 * Strictly validate + normalize an endpoint URL by hand (no URL parser side
 * effects beyond what is needed). Requirements:
 *  - scheme must be exactly https
 *  - no userinfo (username/password)
 *  - no query, no fragment
 *  - port must be absent or 443
 *  - host must be non-empty, no surrounding whitespace
 * Returns null when the endpoint is not acceptable.
 */
function normalizeEndpoint(raw: string): NormalizedEndpoint | null {
  if (typeof raw !== "string") return null;
  const trimmed = raw.trim();
  if (trimmed.length === 0) return null;

  // Reject any query / fragment outright.
  if (trimmed.includes("?") || trimmed.includes("#")) return null;

  const schemeSep = trimmed.indexOf("://");
  if (schemeSep <= 0) return null;
  const scheme = trimmed.slice(0, schemeSep);
  if (scheme.toLowerCase() !== "https") return null;

  const rest = trimmed.slice(schemeSep + 3);
  if (rest.length === 0) return null;

  // No userinfo allowed.
  if (rest.includes("@")) return null;

  const slash = rest.indexOf("/");
  const authority = slash === -1 ? rest : rest.slice(0, slash);
  const rawPath = slash === -1 ? "" : rest.slice(slash);

  if (authority.length === 0) return null;

  let host = authority;
  let port: string | null = null;
  const colon = authority.lastIndexOf(":");
  if (colon !== -1) {
    host = authority.slice(0, colon);
    port = authority.slice(colon + 1);
    if (port !== "443") return null;
  }

  if (host.length === 0) return null;
  // Host must be a plain hostname / IPv4-ish token: letters, digits, dot, hyphen.
  if (!/^[A-Za-z0-9.-]+$/.test(host)) return null;

  // Normalize path: allow exactly one trailing slash, then drop it.
  let path = rawPath;
  if (path.endsWith("/")) {
    path = path.slice(0, -1);
  }
  if (path === "") path = "";

  return { host: host.toLowerCase(), path };
}

interface CatalogEntry {
  readonly hosts: readonly string[];
  readonly paths: readonly string[];
  readonly kinds: readonly string[];
  readonly authModes: readonly string[];
  readonly catalog: OfficialModelCatalog;
}

function freezeModel(id: string, input_images: boolean): OfficialModel {
  return Object.freeze({ id, input_images });
}

function freezeCatalog(
  label: string,
  source: string,
  models: readonly OfficialModel[],
): OfficialModelCatalog {
  return Object.freeze({
    label,
    source,
    checkedAt: CHECKED_AT,
    models: Object.freeze(models.map((m) => freezeModel(m.id, m.input_images))),
  });
}

/**
 * Each entry lists the exact accepted hosts, exact accepted paths (normalized,
 * without trailing slash), accepted kinds and accepted auth modes.
 * Matching is exact: lookalike domains, extra path segments, userinfo, query
 * strings and unknown endpoints all resolve to no catalog.
 */
const ENTRIES: readonly CatalogEntry[] = [
  // 1. 百炼 Token Plan 个人版 (subscription)
  {
    hosts: ["token-plan.cn-beijing.maas.aliyuncs.com"],
    paths: ["/compatible-mode/v1", "/apps/anthropic/v1"],
    kinds: ["subscription"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "百炼 Token Plan 个人版官网候选",
      "https://help.aliyun.com/zh/model-studio/token-plan-personal-overview",
      [
        freezeModel("qwen3.8-max", true),
        freezeModel("qwen3.8-flash", true),
        freezeModel("qwen3.7-max", false),
        freezeModel("qwen3.7-plus", true),
        freezeModel("qwen3.6-flash", true),
        freezeModel("deepseek-v4.1-flash", true),
        freezeModel("deepseek-v4-pro", false),
        freezeModel("deepseek-v4-pro-0813", false),
        freezeModel("deepseek-v4-flash-0731", false),
        freezeModel("glm-5.3", false),
        freezeModel("glm-5.2", false),
      ],
    ),
  },
  // 2. GLM Coding Plan (subscription)
  {
    hosts: ["open.bigmodel.cn"],
    paths: ["/api/coding/paas/v4", "/api/anthropic/v1", "/api/v1"],
    kinds: ["subscription"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "GLM Coding Plan 官网候选",
      "https://docs.bigmodel.cn/cn/coding-plan/tool/others",
      [freezeModel("glm-5.2", false)],
    ),
  },
  // 3. GLM BigModel plain pay-as-you-go API (api) - separate source from the
  //    Coding Plan subscription entry above; kind keeps the two isolated.
  {
    hosts: ["open.bigmodel.cn"],
    paths: ["/api/paas/v4"],
    kinds: ["api"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "GLM BigModel API 官网候选",
      "https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2",
      [freezeModel("glm-5.2", false)],
    ),
  },
  // 4. Kimi Coding Plan (subscription)
  {
    hosts: ["api.kimi.com"],
    paths: ["/coding/v1"],
    kinds: ["subscription"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "Kimi Coding Plan 官网候选",
      "https://www.kimi.com/code/docs/",
      [
        freezeModel("k3", false),
        freezeModel("k3-256k", false),
        freezeModel("kimi-for-coding", false),
        freezeModel("kimi-for-coding-highspeed", false),
      ],
    ),
  },
  // 4. DeepSeek API (api)
  {
    hosts: ["api.deepseek.com"],
    paths: ["/chat/completions", "/responses", "/anthropic/v1", ""],
    kinds: ["api"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "DeepSeek API 官网候选",
      "https://api-docs.deepseek.com/api/create-chat-completion/",
      [freezeModel("deepseek-flash", false), freezeModel("deepseek-v4-pro", false)],
    ),
  },
  // 5. MiniMax API (api)
  {
    hosts: ["api.minimax.cn"],
    paths: ["/v1", "/anthropic/v1"],
    kinds: ["api"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "MiniMax API 官网候选",
      "https://platform.minimax.cn/docs/api-reference/text-openai-api",
      [
        freezeModel("MiniMax-M3", true),
        freezeModel("MiniMax-M2.7", false),
        freezeModel("MiniMax-M2.7-highspeed", false),
        freezeModel("MiniMax-M2.5", false),
        freezeModel("MiniMax-M2.5-highspeed", false),
        freezeModel("MiniMax-M2.1", false),
        freezeModel("MiniMax-M2.1-highspeed", false),
        freezeModel("MiniMax-M2", false),
      ],
    ),
  },
  // 6. MiniMax Token Plan (subscription) - same endpoints, M3 only.
  {
    hosts: ["api.minimax.cn"],
    paths: ["/v1", "/anthropic/v1"],
    kinds: ["subscription"],
    authModes: ["api_key"],
    catalog: freezeCatalog(
      "MiniMax Token Plan 官网候选",
      "https://platform.minimax.cn/docs/token-plan/other-tools",
      [freezeModel("MiniMax-M3", true)],
    ),
  },
];

/**
 * Look up the official candidate catalog for a provider descriptor.
 *
 * Matching rules: exact host, exact normalized path, exact kind, exact
 * auth_mode. Only `auth_mode === "api_key"` can produce a catalog.
 * Returns undefined for unknown / malformed / oauth / local providers.
 *
 * The returned object (and its nested models) is deep-frozen; mutating it
 * throws in strict mode and cannot affect subsequent lookups.
 */
export function officialModelsForProvider(
  provider: { endpoint: string; kind: string; auth_mode: string },
): OfficialModelCatalog | undefined {
  if (provider === null || typeof provider !== "object") return undefined;
  const { endpoint, kind, auth_mode } = provider;
  if (typeof endpoint !== "string") return undefined;
  if (typeof kind !== "string") return undefined;
  if (typeof auth_mode !== "string") return undefined;
  if (auth_mode !== "api_key") return undefined;

  const norm = normalizeEndpoint(endpoint);
  if (norm === null) return undefined;

  for (const entry of ENTRIES) {
    if (!entry.hosts.includes(norm.host)) continue;
    if (!entry.paths.includes(norm.path)) continue;
    if (!entry.kinds.includes(kind)) continue;
    if (!entry.authModes.includes(auth_mode)) continue;
    return entry.catalog;
  }
  return undefined;
}

const SAFE_ID_CHAR = /[A-Za-z0-9._:\-]/;
const MAX_SUGGEST_LEN = 128;

/**
 * Build a safe public model ID: `${providerID}/${upstreamID}`.
 *
 * Rules:
 *  - allowed characters: ASCII letters, digits, space-free set `._:-` and `/`
 *  - total length <= 128
 *  - no empty segment, no `..`, no leading or trailing `/`
 *  - every `/`-separated segment must START with an ASCII letter or digit
 *    (leading `._:-` are rejected; they are fine inside a segment)
 *  - upstream casing preserved; no aliasing / normalization of model names
 * Invalid input yields the empty string.
 */
export function suggestModelID(providerID: string, upstreamID: string): string {
  if (typeof providerID !== "string" || typeof upstreamID !== "string") {
    return "";
  }
  const candidate = `${providerID}/${upstreamID}`;
  if (candidate.length === 0 || candidate.length > MAX_SUGGEST_LEN) return "";
  if (candidate.startsWith("/") || candidate.endsWith("/")) return "";
  if (candidate.includes("..")) return "";

  for (const ch of candidate) {
    if (!SAFE_ID_CHAR.test(ch) && ch !== "/") return "";
  }

  for (const segment of candidate.split("/")) {
    if (segment.length === 0) return "";
    // Backend safeID extra rule: first character of each slash-separated
    // segment must be an ASCII letter or digit.
    if (!/^[A-Za-z0-9]/.test(segment)) return "";
  }

  return candidate;
}
