import type { Provider, RequestRecord, Summary } from "../api";
import { formatNumber } from "../lib/format";
import { RequestTable } from "./RequestTable";

const DASH = "\u2014";

const KIND_LABEL: Record<Provider["kind"], string> = {
  api: "API 直连",
  subscription: "订阅套餐",
  local: "本地服务",
};

const AUTH_LABEL: Record<Provider["auth_mode"], string> = {
  api_key: "API Key",
  oauth: "订阅登录",
  none: "无需鉴权",
};

/**
 * Source cell: the provider's own kind plus the configured access method, e.g.
 * "订阅套餐 · API Key" (Bailian Token Plan style key-based subscription) or
 * "本地服务 · 无需鉴权". Wording deliberately never claims a live login or a
 * healthy upstream - only how the upstream is wired in.
 */
function sourceLabel(provider: Provider): string {
  const kind = KIND_LABEL[provider.kind];
  const auth = AUTH_LABEL[provider.auth_mode];
  if (!kind) return DASH;
  return auth ? `${kind} · ${auth}` : kind;
}

const CONFIG_LABEL: Record<"enabled" | "disabled", string> = {
  enabled: "已启用",
  disabled: "已停用",
};

export interface OverviewProps {
  summary: Summary | null;
  providers: Provider[];
  records: RequestRecord[];
  onNavigate: (page: "providers" | "models" | "callers" | "requests") => void;
}

export function Overview({ summary, providers, records, onNavigate }: OverviewProps) {
  const knownTokens = summary ? summary.input_tokens + summary.output_tokens : null;

  return (
    <div className="overview">
      <header className="overview-header">
        <div className="overview-title">
          <h1>总览</h1>
          <p className="muted">统一管理模型入口，清楚了解每一次调用。</p>
        </div>
        <button type="button" className="button button-primary" onClick={() => onNavigate("providers")}>
          添加上游
        </button>
      </header>

      <section className="metrics" aria-label="关键指标">
        <div className="metric">
          <span className="metric-label">累计请求</span>
          <span className="metric-value mono">{summary ? formatNumber(summary.requests) : DASH}</span>
        </div>
        <div className="metric">
          <span className="metric-label">已知 Tokens</span>
          <span className="metric-value mono">{summary ? formatNumber(knownTokens) : DASH}</span>
          <span className="metric-note muted">
            {summary
              ? `本网关已知用量 · ${formatNumber(summary.unknown_usage_requests)} 条调用用量未知`
              : DASH}
          </span>
        </div>
        <div className="metric">
          <span className="metric-label">已启用上游</span>
          <span className="metric-value mono">
            {summary
              ? `${formatNumber(summary.ready_providers)} / ${formatNumber(summary.providers)}`
              : DASH}
          </span>
          <span className="metric-note muted">就绪 / 已配置，不代表实时健康</span>
        </div>
      </section>

      <div className="overview-grid">
        <section className="panel" aria-label="上游账号">
          <div className="panel-header">
            <h2>上游账号</h2>
            <button type="button" className="button" onClick={() => onNavigate("providers")}>
              管理上游
            </button>
          </div>
          {providers.length === 0 ? (
            <div className="empty-state">
              <p>尚未配置上游账号</p>
              <p className="muted">支持订阅登录、API Key 与本地推理服务</p>
            </div>
          ) : (
            <div className="table-scroll">
              <table className="provider-table">
                <thead>
                  <tr>
                    <th scope="col">供应商</th>
                    <th scope="col">来源</th>
                    <th scope="col">协议</th>
                    <th scope="col">配置状态</th>
                  </tr>
                </thead>
                <tbody>
                  {providers.map((provider) => (
                    <tr key={provider.id}>
                      <td>{provider.name || DASH}</td>
                      <td>{sourceLabel(provider)}</td>
                      <td className="mono">{provider.protocol}</td>
                      <td className={provider.enabled ? "status status-success" : "muted"}>
                        {provider.enabled ? CONFIG_LABEL.enabled : CONFIG_LABEL.disabled}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <aside className="panel guide" aria-label="接入指南">
          <div className="panel-header">
            <h2>接入指南</h2>
          </div>
          <ol className="guide-steps">
            <li className="guide-step">
              <span className="guide-step-title">1 添加上游</span>
              <p className="muted">接入订阅账号、API Key 或本地推理服务。</p>
              <button type="button" className="button" onClick={() => onNavigate("providers")}>
                添加上游
              </button>
            </li>
            <li className="guide-step">
              <span className="guide-step-title">2 配置模型</span>
              <p className="muted">映射对外模型名与上游模型。</p>
              <button type="button" className="button" onClick={() => onNavigate("models")}>
                配置模型
              </button>
            </li>
            <li className="guide-step">
              <span className="guide-step-title">3 创建调用方 Key</span>
              <p className="muted">为应用签发可限流的调用方 Key。</p>
              <button type="button" className="button" onClick={() => onNavigate("callers")}>
                创建调用方
              </button>
            </li>
          </ol>
        </aside>
      </div>

      <section className="panel" aria-label="最近调用">
        <div className="panel-header">
          <h2>最近调用</h2>
          <button type="button" className="button" onClick={() => onNavigate("requests")}>
            查看全部
          </button>
        </div>
        <RequestTable records={records} compact />
      </section>
    </div>
  );
}
