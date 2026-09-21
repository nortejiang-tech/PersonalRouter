import { useCallback, useState } from "react";
import type { ReactNode } from "react";
import {
  createClient,
  type AdminClient,
  type Caller,
  type Model,
  type Provider,
  type RequestFilter,
  type RequestRecord,
  type Settings,
  type Summary,
} from "./api";
import { CallerPage } from "./components/CallerPage";
import { ModelPage } from "./components/ModelPage";
import { Overview } from "./components/Overview";
import { ProviderPage } from "./components/ProviderPage";
import { RequestTable } from "./components/RequestTable";
import { copyText } from "./lib/clipboard";

type Page = "overview" | "providers" | "models" | "callers" | "requests" | "settings";

interface AppData {
  summary: Summary;
  providers: Provider[];
  models: Model[];
  callers: Caller[];
  records: RequestRecord[];
  settings: Settings;
}

const NAV_ITEMS: Array<{ page: Page; label: string; icon: IconName }> = [
  { page: "overview", label: "总览", icon: "grid" },
  { page: "providers", label: "上游账号", icon: "plug" },
  { page: "models", label: "模型", icon: "layers" },
  { page: "callers", label: "调用方与 Key", icon: "key" },
  { page: "requests", label: "调用记录", icon: "list" },
  { page: "settings", label: "设置", icon: "settings" },
];

const PAGE_TITLES: Record<Page, string> = {
  overview: "总览",
  providers: "上游账号",
  models: "模型",
  callers: "调用方与 Key",
  requests: "调用记录",
  settings: "设置",
};

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : "管理请求失败，请稍后重试";
}

type IconName = "grid" | "plug" | "layers" | "key" | "list" | "settings" | "logout";

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    grid: <><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></>,
    plug: <><path d="M9 7V3M15 7V3M6 7h12v3a6 6 0 0 1-12 0V7Zm6 9v5" /><path d="M9 21h6" /></>,
    layers: <><path d="m12 3 9 5-9 5-9-5 9-5Z" /><path d="m3 12 9 5 9-5M3 16l9 5 9-5" /></>,
    key: <><circle cx="8" cy="15" r="4" /><path d="m11 12 8-8m-2 2 3 3m-6 0 3 3" /></>,
    list: <><path d="M8 6h13M8 12h13M8 18h13" /><path d="M3 6h.01M3 12h.01M3 18h.01" /></>,
    settings: <><path d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z" /><path d="m19.4 15 .1.1a2 2 0 1 1-2.8 2.8l-.1-.1a2 2 0 0 0-3.4 1.4v.3a2 2 0 1 1-4 0v-.2A2 2 0 0 0 5.8 18l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A2 2 0 0 0 1.6 12H1.5a2 2 0 1 1 0-4h.2A2 2 0 0 0 3 4.6l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A2 2 0 0 0 9.2.5V.4a2 2 0 1 1 4 0v.2A2 2 0 0 0 16.6 2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1A2 2 0 0 0 20.8 8h.2a2 2 0 1 1 0 4h-.2a2 2 0 0 0-1.4 3Z" /></>,
    logout: <><path d="M10 17l5-5-5-5M15 12H3" /><path d="M21 19V5a2 2 0 0 0-2-2h-6" /></>,
  };
  return <svg className="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}

function Login({ onLogin, busy, error }: { onLogin: (key: string) => Promise<void>; busy: boolean; error: string }) {
  const [key, setKey] = useState("");
  const [commandMessage, setCommandMessage] = useState("");
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    await onLogin(key);
  };
  const adminKeyCommand = "ssh <gateway-host> 'sudo -n cat <protected-admin-key-path>' | pbcopy";
  const copyCommand = async () => {
    setCommandMessage((await copyText(adminKeyCommand)) ? "命令已复制到剪贴板" : "复制失败，请在终端中手动选择命令");
  };
  return (
    <main className="login-shell">
      <section className="login-card" aria-labelledby="login-title">
        <div className="brand-mark">PR</div>
        <p className="eyebrow">PersonalRouter 管理控制台</p>
        <h1 id="login-title">安全登录</h1>
        <p className="muted">输入管理员 Key 访问本机管理接口。Key 仅保存在当前页面内存中。</p>
        <form className="login-form" onSubmit={submit}>
          <label className="field">管理员 Key<input type="password" autoComplete="current-password" value={key} onChange={(event) => setKey(event.target.value)} required /><span className="field-hint">将获取到的管理员 Key 粘贴到这里；Key 不会写入地址栏或浏览器存储。</span></label>
          <div className="field"><span>从已配置网关的终端获取 Key</span><code className="copy-value">{adminKeyCommand}</code><span className="field-hint">将命令中的网关主机和受保护 Key 路径替换为部署值，再在已授权的终端运行。按钮只复制命令，不会执行。</span><button type="button" className="button button-small" onClick={() => void copyCommand()}>复制获取命令</button>{commandMessage && <span className="status status-info" role="status">{commandMessage}</span>}</div>
          {error && <p className="status status-error" role="alert">{error}</p>}
          <button type="submit" className="button button-primary button-block" disabled={busy}>{busy ? "验证中…" : "进入控制台"}</button>
        </form>
      </section>
    </main>
  );
}

export default function App() {
  const [page, setPage] = useState<Page>("overview");
  const [client, setClient] = useState<AdminClient | null>(null);
  const [adminKey, setAdminKey] = useState("");
  const [data, setData] = useState<AppData | null>(null);
  const [requestRows, setRequestRows] = useState<RequestRecord[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<RequestFilter>({ limit: 100 });

  const loadData = useCallback(async (activeClient: AdminClient) => {
    setBusy(true);
    setError("");
    try {
      const [summary, providers, models, callers, records, settings] = await Promise.all([
        activeClient.overview(),
        activeClient.providers(),
        activeClient.models(),
        activeClient.callers(),
        activeClient.requests({ limit: 100 }),
        activeClient.settings(),
      ]);
      setData({ summary, providers, models, callers, records, settings });
      setRequestRows(records);
    } catch (loadError) {
      setError(errorMessage(loadError));
      throw loadError;
    } finally {
      setBusy(false);
    }
  }, []);

  const handleLogin = useCallback(async (rawKey: string) => {
    setBusy(true);
    setError("");
    try {
      const nextClient = createClient(rawKey);
      await loadData(nextClient);
      setAdminKey(rawKey);
      setClient(nextClient);
      setPage("overview");
    } catch (loginError) {
      setData(null);
      setRequestRows([]);
      setError(errorMessage(loginError));
    } finally {
      setBusy(false);
    }
  }, [loadData]);

  const refresh = useCallback(async () => {
    if (client) await loadData(client);
  }, [client, loadData]);

  const logout = () => {
    setClient(null);
    setAdminKey("");
    setData(null);
    setRequestRows([]);
    setError("");
    setPage("overview");
  };

  const runRequestFilter = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!client) return;
    setBusy(true);
    setError("");
    try {
      const rows = await client.requests({ ...filter, limit: 100 });
      setRequestRows(rows);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setBusy(false);
    }
  };

  if (!client || !data) return <Login onLogin={handleLogin} busy={busy} error={error} />;

  const currentTitle = PAGE_TITLES[page];
  const onChange = async () => { await refresh(); };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="sidebar-brand"><div className="brand-mark small">PR</div><div><strong>PersonalRouter</strong><span>管理控制台</span></div></div>
        <nav className="main-nav" aria-label="主导航">
          {NAV_ITEMS.map((item) => <button key={item.page} type="button" className={`nav-item ${page === item.page ? "active" : ""}`} onClick={() => setPage(item.page)}><Icon name={item.icon} /><span>{item.label}</span></button>)}
        </nav>
        <div className="sidebar-footer"><button type="button" className="nav-item logout-button" onClick={logout}><Icon name="logout" /><span>退出登录</span></button><span className="sidebar-note">Key 仅存于当前页面</span></div>
      </aside>

      <main className="content-shell">
        <header className="content-header"><p className="eyebrow">PersonalRouter / {currentTitle}</p><button type="button" className="button" onClick={() => { void refresh().catch(() => undefined); }} disabled={busy}>{busy ? "刷新中…" : "刷新数据"}</button></header>
        {error && <div className="global-error status status-error" role="alert">{error}</div>}

        {page === "overview" && <Overview summary={data.summary} providers={data.providers} records={requestRows} onNavigate={setPage} />}
        {page === "providers" && <ProviderPage client={client} providers={data.providers} models={data.models} callers={data.callers} onChange={onChange} />}
        {page === "models" && <ModelPage client={client} providers={data.providers} models={data.models} callers={data.callers} onChange={onChange} />}
        {page === "callers" && <CallerPage client={client} providers={data.providers} models={data.models} callers={data.callers} settings={data.settings} onChange={onChange} />}
        {page === "requests" && <RequestsPage client={client} rows={requestRows} callers={data.callers} models={data.models} filter={filter} setFilter={setFilter} busy={busy} onSubmit={runRequestFilter} />}
        {page === "settings" && <SettingsPage settings={data.settings} />}
      </main>
    </div>
  );
}

function RequestsPage({ client: _client, rows, callers, models, filter, setFilter, busy, onSubmit }: { client: AdminClient; rows: RequestRecord[]; callers: Caller[]; models: Model[]; filter: RequestFilter; setFilter: React.Dispatch<React.SetStateAction<RequestFilter>>; busy: boolean; onSubmit: (event: React.FormEvent<HTMLFormElement>) => Promise<void> }) {
  return <section className="page requests-page" aria-labelledby="requests-title">
    <div className="page-heading"><div><p className="eyebrow">调用记录</p><h2 id="requests-title">请求审计</h2><p className="muted">筛选最近记录，未知用量会保持为空而不会被当作零。</p></div></div>
    <form className="panel filter-bar" onSubmit={onSubmit}>
      <label className="field">调用方<select value={filter.caller_id ?? ""} onChange={(event) => setFilter((current) => ({ ...current, caller_id: event.target.value || undefined }))}><option value="">全部调用方</option>{callers.map((caller) => <option key={caller.id} value={caller.id}>{caller.name}（{caller.id}）</option>)}</select><span className="field-hint">按调用方名称或内部 ID 选择。</span></label>
      <label className="field">模型<select value={filter.model ?? ""} onChange={(event) => setFilter((current) => ({ ...current, model: event.target.value || undefined }))}><option value="">全部模型</option>{models.map((model) => <option key={model.id} value={model.id}>{model.name}（{model.id}）</option>)}</select><span className="field-hint">发送给服务端的是完整公开模型 ID。</span></label>
      <label className="field">HTTP 状态<select value={filter.status ?? ""} onChange={(event) => setFilter((current) => ({ ...current, status: event.target.value ? Number(event.target.value) : undefined }))}><option value="">全部状态</option>{[200, 400, 401, 403, 404, 409, 429, 500, 502, 503].map((status) => <option key={status} value={status}>{status}</option>)}</select><span className="field-hint">选择常见状态码，或查看全部记录。</span></label>
      <div className="actions filter-actions"><button type="submit" className="button button-primary" disabled={busy}>{busy ? "查询中…" : "应用筛选"}</button></div>
    </form>
    <section className="panel"><RequestTable records={rows} /></section>
  </section>;
}

function SettingsPage({ settings }: { settings: Settings }) {
  const [message, setMessage] = useState("");
  const lanURL = settings.lan_base_url;
  const publicURL = settings.public_base_url;
  const copy = async (value: string | undefined, label: string) => {
    if (!value) return;
    setMessage((await copyText(value)) ? `${label}已复制到剪贴板` : `${label}复制失败，请手动选择地址`);
  };
  const modelExample = lanURL ? `${lanURL.replace(/\/$/, "")}/chat/completions` : "等待服务端返回模型入口";
  return <section className="page settings-page" aria-labelledby="settings-title">
    <div className="page-heading"><div><p className="eyebrow">设置</p><h2 id="settings-title">连接与接入</h2><p className="muted">地址来自服务端当前配置；页面不会猜测或替换这些地址。</p></div></div>
    <section className="panel settings-grid">
      <div><h3>管理接口状态</h3><p className="status status-success">已通过管理员 Key 连接</p><p className="muted">服务端已返回当前配置，可以继续管理上游、模型和调用方。</p></div>
      <div><h3>LAN Base URL</h3><code className="copy-value">{lanURL ?? "服务端未返回"}</code><button type="button" className="button button-small" onClick={() => void copy(lanURL, "LAN 地址")} disabled={!lanURL}>复制</button></div>
      <div><h3>公网 Base URL</h3><code className="copy-value">{publicURL ?? "服务端未返回"}</code><button type="button" className="button button-small" onClick={() => void copy(publicURL, "公网地址")} disabled={!publicURL}>复制</button></div>
      <div className="field-wide"><h3>模型调用示例</h3><pre className="code-block"><code>{`curl ${modelExample} \\\n  -H 'Authorization: Bearer YOUR_CALLER_KEY' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"model":"YOUR_MODEL_ID","messages":[{"role":"user","content":"hello"}]}'`}</code></pre><p className="muted">示例中的占位符不会读取或填入管理员 Key。</p></div>
    </section>
    {message && <p className="status status-info" role="status">{message}</p>}
  </section>;
}
