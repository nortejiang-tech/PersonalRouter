/**
 * EVO-005 供应商协议与端点目录（纯数据）。
 *
 * 仅用于前端提示与表单默认值。本模块绝不自动创建、启用账号，
 * 也不含任何 key/token 字段、网络访问或副作用。
 * 每条记录的 verification 恒为 'PENDING'：专指账户真实调用未验收，
 * 不代表官网配置未知。
 */

export type ProviderKind = 'api' | 'subscription' | 'local';
export type ProviderAuthMode = 'api_key' | 'oauth' | 'none';
export type ProviderProtocol = 'openai' | 'responses' | 'anthropic' | 'adapter';
export type ProviderOauthProvider = 'codex' | 'kimi';
export type ProviderConfigSource = 'official' | 'adapter' | 'deployment';

/** 单个协议到端点的映射，端点由官网核实，直接拼接使用，不依赖 SDK 推断。 */
export interface ProviderPresetConnection {
  readonly protocol: ProviderProtocol;
  readonly endpoint: string;
  readonly source: string;
}

export interface ProviderPreset {
  readonly id: string;
  readonly label: string;
  readonly kind: ProviderKind;
  readonly authMode: ProviderAuthMode;
  readonly protocol: ProviderProtocol;
  readonly endpoint: string;
  readonly oauthProvider?: ProviderOauthProvider;
  readonly note: string;
  readonly source: string;
  readonly verification: 'PENDING';
  readonly connections: readonly ProviderPresetConnection[];
  readonly configSource: ProviderConfigSource;
  readonly verifiedAt: string;
  readonly managedEndpoint: boolean;
}

const VERIFIED_AT = '2026-09-20';

function connection(
  protocol: ProviderProtocol,
  endpoint: string,
  source: string,
): ProviderPresetConnection {
  return Object.freeze({ protocol, endpoint, source });
}

function preset(input: {
  id: string;
  label: string;
  kind: ProviderKind;
  authMode: ProviderAuthMode;
  protocol: ProviderProtocol;
  endpoint: string;
  oauthProvider?: ProviderOauthProvider;
  note: string;
  connections: readonly ProviderPresetConnection[];
  configSource: ProviderConfigSource;
}): ProviderPreset {
  const defaults = input.connections.find((c) => c.protocol === input.protocol)!;
  return Object.freeze({
    id: input.id,
    label: input.label,
    kind: input.kind,
    authMode: input.authMode,
    protocol: input.protocol,
    endpoint: input.endpoint,
    ...(input.oauthProvider ? { oauthProvider: input.oauthProvider } : {}),
    note: input.note,
    source: defaults.source,
    verification: 'PENDING' as const,
    connections: Object.freeze([...input.connections]),
    configSource: input.configSource,
    verifiedAt: VERIFIED_AT,
    managedEndpoint: input.authMode === 'oauth',
  });
}

export const PROVIDER_PRESETS: readonly ProviderPreset[] = Object.freeze([
  preset({
    id: 'codex-subscription',
    label: 'Codex 订阅',
    kind: 'subscription',
    authMode: 'oauth',
    protocol: 'adapter',
    endpoint: '',
    oauthProvider: 'codex',
    note: '账号授权由本人完成，订阅与 OpenAI API 独立，端点自动配置。',
    connections: [
      connection(
        'adapter',
        '',
        'https://developers.openai.com/codex/auth/',
      ),
    ],
    configSource: 'adapter',
  }),
  preset({
    id: 'glm-coding',
    label: 'GLM Coding Plan',
    kind: 'subscription',
    authMode: 'api_key',
    protocol: 'openai',
    endpoint: 'https://open.bigmodel.cn/api/coding/paas/v4',
    note: '套餐专用，按协议自动匹配地址，不自动转按量 API。',
    connections: [
      connection(
        'openai',
        'https://open.bigmodel.cn/api/coding/paas/v4',
        'https://docs.bigmodel.cn/cn/coding-plan/tool/others',
      ),
      connection(
        'anthropic',
        'https://open.bigmodel.cn/api/anthropic/v1',
        'https://docs.bigmodel.cn/cn/coding-plan/tool/others',
      ),
      connection(
        'responses',
        'https://open.bigmodel.cn/api/v1',
        'https://docs.bigmodel.cn/cn/coding-plan/tool/others',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'glm-api',
    label: 'GLM BigModel API',
    kind: 'api',
    authMode: 'api_key',
    protocol: 'openai',
    endpoint: 'https://open.bigmodel.cn/api/paas/v4',
    note: '普通按量 API，与 Coding Plan 分开。',
    connections: [
      connection(
        'openai',
        'https://open.bigmodel.cn/api/paas/v4',
        'https://docs.bigmodel.cn/cn/best-practice/case/ai-search-engine',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'bailian-token-plan',
    label: '百炼 Token Plan',
    kind: 'subscription',
    authMode: 'api_key',
    protocol: 'openai',
    endpoint: 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    note: '使用 Token Plan 专属 Key，与百炼 Coding Plan 及普通 DashScope 分开。',
    connections: [
      connection(
        'openai',
        'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
        'https://help.aliyun.com/zh/model-studio/more-tools',
      ),
      connection(
        'anthropic',
        'https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1',
        'https://help.aliyun.com/en/model-studio/opencode',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'kimi-coding',
    label: 'Kimi Coding Plan',
    kind: 'subscription',
    authMode: 'api_key',
    protocol: 'anthropic',
    endpoint: 'https://api.kimi.com/coding/v1',
    note: '使用 Kimi Code 订阅 Key，与 Moonshot 按量 API 分开。',
    connections: [
      connection(
        'anthropic',
        'https://api.kimi.com/coding/v1',
        'https://www.kimi.com/code/docs/',
      ),
      connection(
        'openai',
        'https://api.kimi.com/coding/v1',
        'https://www.kimi.com/code/docs/en/third-party-tools/hermes.html',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'kimi-login',
    label: 'Kimi 订阅登录',
    kind: 'subscription',
    authMode: 'oauth',
    protocol: 'adapter',
    endpoint: '',
    oauthProvider: 'kimi',
    note: '本人完成设备授权，端点自动配置。',
    connections: [
      connection(
        'adapter',
        '',
        'https://www.kimi.com/code/docs/en/kimi-code-cli/configuration/providers',
      ),
    ],
    configSource: 'adapter',
  }),
  preset({
    id: 'deepseek-api',
    label: 'DeepSeek API',
    kind: 'api',
    authMode: 'api_key',
    protocol: 'openai',
    endpoint: 'https://api.deepseek.com/chat/completions',
    note: '按协议自动匹配，具体模型能力以官方说明为准。',
    connections: [
      connection(
        'openai',
        'https://api.deepseek.com/chat/completions',
        'https://api-docs.deepseek.com/',
      ),
      connection(
        'anthropic',
        'https://api.deepseek.com/anthropic/v1',
        'https://api-docs.deepseek.com/guides/anthropic_api/',
      ),
      connection(
        'responses',
        'https://api.deepseek.com/responses',
        'https://api-docs.deepseek.com/guides/responses_api/',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'minimax-api',
    label: 'MiniMax API',
    kind: 'api',
    authMode: 'api_key',
    protocol: 'openai',
    endpoint: 'https://api.minimax.cn/v1',
    note: '中国站按量 API，使用普通 API Key，与订阅 Key 分开。',
    connections: [
      connection(
        'openai',
        'https://api.minimax.cn/v1',
        'https://platform.minimax.cn/docs/api-reference/text-openai-api',
      ),
      connection(
        'anthropic',
        'https://api.minimax.cn/anthropic/v1',
        'https://platform.minimax.cn/docs/api-reference/text-anthropic-api',
      ),
      connection(
        'responses',
        'https://api.minimax.cn/v1',
        'https://platform.minimax.cn/docs/api-reference/responses-create',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'minimax-token-plan',
    label: 'MiniMax Token Plan',
    kind: 'subscription',
    authMode: 'api_key',
    protocol: 'anthropic',
    endpoint: 'https://api.minimax.cn/anthropic/v1',
    note: '使用 Token Plan 专属订阅 Key，不自动转按量 API。',
    connections: [
      connection(
        'anthropic',
        'https://api.minimax.cn/anthropic/v1',
        'https://platform.minimax.cn/docs/token-plan/other-tools',
      ),
      connection(
        'openai',
        'https://api.minimax.cn/v1',
        'https://platform.minimax.cn/docs/token-plan/other-tools',
      ),
    ],
    configSource: 'official',
  }),
  preset({
    id: 'evo-local',
    label: 'EVO 本地模型',
    kind: 'local',
    authMode: 'none',
    protocol: 'openai',
    endpoint: 'http://127.0.0.1:8001/v1',
    note: 'EVO 现有推理服务入口，可手动修改，不自动加载或切换模型。',
    connections: [connection('openai', 'http://127.0.0.1:8001/v1', '')],
    configSource: 'deployment',
  }),
]);

/**
 * 按 id 返回一份深拷贝（connections 数组与每项均独立），
 * 避免调用者修改污染目录常量。
 */
export function getPreset(id: string): ProviderPreset | undefined {
  const found = PROVIDER_PRESETS.find((preset) => preset.id === id);
  if (!found) return undefined;
  const copy: Record<string, unknown> = { ...found };
  copy.connections = Object.freeze(
    found.connections.map((c) => ({ ...c })),
  );
  return copy as unknown as ProviderPreset;
}
