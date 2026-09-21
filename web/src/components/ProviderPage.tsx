import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { AdminClient, Caller, Model, OAuthAccount, OAuthProvider, OAuthStatus, Provider, ProviderInput, Settings } from "../api";
import { PROVIDER_PRESETS } from "../lib/presets";
import { applyPresetSelection, connectionForProtocol, getPresetWithConnections, nextPresetSelection, protocolChoices, sourceLabel, type ProviderPresetWithConnections } from "../lib/provider-preset";
import { ProviderTools } from "./ProviderTools";

type ProviderDraft = Omit<ProviderInput, "id"> & { id: string; secret: string; remove_secret: boolean };
interface ProviderPageProps { client: AdminClient; providers: Provider[]; models: Model[]; callers: Caller[]; onChange: () => Promise<void> }

const emptyDraft = (): ProviderDraft => ({ id: "", name: "", kind: "api", auth_mode: "api_key", protocol: "openai", endpoint: "", enabled: false, oauth_provider: "", secret: "", remove_secret: false });
function draftFromProvider(provider: Provider): ProviderDraft { return { id: provider.id, name: provider.name, kind: provider.kind, auth_mode: provider.auth_mode, protocol: provider.protocol, endpoint: provider.auth_mode === "oauth" && provider.protocol === "adapter" ? "" : provider.endpoint, enabled: provider.enabled, oauth_provider: provider.oauth_provider, secret: "", remove_secret: false }; }
function displayError(error: unknown): string { return error instanceof Error ? error.message : "操作失败，请稍后重试"; }
const ACCOUNT_LABELS: Record<OAuthAccount["status"], string> = { missing: "未登录", active: "已授权", disabled: "已停用", expired: "已过期", error: "授权异常", unknown: "状态未知" };
function accountUsable(account: OAuthAccount | undefined): boolean { return account?.account_count === 1 && account.status === "active"; }
function providerFor(value: string): OAuthProvider | undefined { return value === "codex" || value === "kimi" ? value : undefined; }
function generatedProviderID(providers: Provider[]): string {
  const existing = new Set(providers.map((provider) => provider.id));
  const base = `provider-${Date.now().toString(36)}`;
  let candidate = base;
  let suffix = 2;
  while (existing.has(candidate)) candidate = `${base}-${suffix++}`;
  return candidate;
}
function availablePresetID(base: string, providers: Provider[]): string {
  const existing = new Set(providers.map((provider) => provider.id));
  if (!existing.has(base)) return base;
  let suffix = 2;
  while (existing.has(`${base}-${suffix}`)) suffix += 1;
  return `${base}-${suffix}`;
}
function matchingPreset(provider: Provider): string {
  return PROVIDER_PRESETS.find((preset) => {
    const details = getPresetWithConnections(preset.id);
    if (!details || details.kind !== provider.kind || details.authMode !== provider.auth_mode) return false;
    if ((details.oauthProvider ?? "") !== provider.oauth_provider) return false;
    const connection = connectionForProtocol(details, provider.protocol);
    if (!connection) return false;
    return details.managedEndpoint || connection.endpoint === provider.endpoint;
  })?.id ?? "";
}

export function ProviderPage({ client, providers, models, callers, onChange }: ProviderPageProps) {
  void models; void callers;
  const initialProviderID = generatedProviderID(providers);
  const [editingID, setEditingID] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderDraft>(() => ({ ...emptyDraft(), id: initialProviderID }));
  const [selectedPreset, setSelectedPreset] = useState("");
  const [generatedPresetID, setGeneratedPresetID] = useState(initialProviderID);
  const [generatedPresetName, setGeneratedPresetName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [settings, setSettings] = useState<Settings | null>(null);
  const [accounts, setAccounts] = useState<Partial<Record<OAuthProvider, OAuthAccount>>>({});
  const [accountBusy, setAccountBusy] = useState<OAuthProvider | "">("");
  const [accountError, setAccountError] = useState("");
  const [oauthState, setOAuthState] = useState("");
  const [oauthURL, setOAuthURL] = useState("");
  const [oauthUserCode, setOAuthUserCode] = useState("");
  const [oauthExpiresIn, setOAuthExpiresIn] = useState<number | undefined>();
  const [oauthDeadline, setOAuthDeadline] = useState<number | null>(null);
  const [oauthStatus, setOAuthStatus] = useState<OAuthStatus | null>(null);
  const [callbackURL, setCallbackURL] = useState("");
  const [oauthBusy, setOAuthBusy] = useState(false);
  const [oauthMessage, setOAuthMessage] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(true);
  const [toolsProviderID, setToolsProviderID] = useState<string | null>(null);

  const readAccount = useCallback(async (provider: OAuthProvider) => {
    try {
      const result = await client.oauthAccount(provider);
      setAccounts((current) => ({ ...current, [provider]: result }));
      setAccountError("");
      return result;
    } catch (accountRequestError) {
      setAccounts((current) => ({ ...current, [provider]: { provider, status: "unknown", account_count: 0, model_prefix: "" } }));
      setAccountError(displayError(accountRequestError));
      throw accountRequestError;
    }
  }, [client]);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const nextSettings = await client.settings();
        if (cancelled) return;
        setSettings(nextSettings);
        const supported = nextSettings.supported_oauth ?? [];
        const wanted = [...new Set(providers.map((item) => providerFor(item.oauth_provider)).filter((item): item is OAuthProvider => Boolean(item)))]
          .filter((provider) => nextSettings.adapter_configured && (supported.length === 0 || supported.includes(provider)));
        const results = await Promise.all(wanted.map(async (provider) => [provider, await client.oauthAccount(provider)] as const));
        if (!cancelled) setAccounts(Object.fromEntries(results));
      } catch (settingsError) {
        if (!cancelled) setAccountError(displayError(settingsError));
      }
    };
    void load();
    return () => { cancelled = true; };
  }, [client, providers]);

  const inspectOAuth = useCallback(async () => {
    if (!oauthState) return null;
    const status = await client.oauthStatus(oauthState);
    setOAuthStatus(status);
    if (status.status === "completed") {
      const provider = providerFor(draft.oauth_provider);
      if (!provider) throw new Error("OAuth 提供商不受支持");
      const account = await readAccount(provider);
      setOAuthMessage(accountUsable(account) ? "授权状态已确认，可以保存启用配置。" : "授权已返回，但账户状态尚未确认可用。");
    } else if (status.status === "failed" || status.status === "cancelled" || status.status === "expired") {
      setOAuthMessage("授权未完成，请重新开始授权。");
    } else {
      setOAuthMessage("授权进行中，等待状态确认。");
    }
    return status;
  }, [client, draft.oauth_provider, oauthState, readAccount]);

  useEffect(() => {
    if (!oauthState || draft.auth_mode !== "oauth") return undefined;
    let stopped = false;
    let timer: number | undefined;
    let attempts = 0;
    let failures = 0;
    const deadline = oauthDeadline ?? Date.now() + 10 * 60 * 1000;
    const poll = async () => {
      if (stopped || attempts >= 120) return;
      attempts += 1;
      try {
        const status = await inspectOAuth();
        if (status && ["completed", "failed", "cancelled", "expired"].includes(status.status ?? "")) return;
        if (!stopped && Date.now() < deadline) timer = window.setTimeout(() => void poll(), 5000);
        else if (!stopped) setOAuthMessage("授权等待已超时，可手动检查或重新开始授权。");
      } catch (pollError) {
        failures += 1;
        if (!stopped && failures < 6 && Date.now() < deadline) timer = window.setTimeout(() => void poll(), 5000);
        else if (!stopped) setOAuthMessage(displayError(pollError));
      }
    };
    void poll();
    return () => { stopped = true; if (timer !== undefined) window.clearTimeout(timer); };
  }, [draft.auth_mode, inspectOAuth, oauthDeadline, oauthState]);

  const resetOAuth = () => { setOAuthState(""); setOAuthURL(""); setOAuthUserCode(""); setOAuthExpiresIn(undefined); setOAuthDeadline(null); setOAuthStatus(null); setCallbackURL(""); setOAuthMessage(""); };
  const beginCreate = () => { const id = generatedProviderID(providers); setEditingID(null); setDraft({ ...emptyDraft(), id }); setSelectedPreset(""); setGeneratedPresetID(id); setGeneratedPresetName(""); setAdvancedOpen(true); setToolsProviderID(null); resetOAuth(); setError(""); setAccountError(""); };
  const beginEdit = (provider: Provider) => { setEditingID(provider.id); setDraft(draftFromProvider(provider)); setSelectedPreset(matchingPreset(provider)); setGeneratedPresetID(""); setGeneratedPresetName(""); setAdvancedOpen(!matchingPreset(provider)); setToolsProviderID(provider.id); resetOAuth(); setError(""); setAccountError(""); const oauthProvider = providerFor(provider.oauth_provider); if (oauthProvider) void readAccount(oauthProvider).catch(() => undefined); };

  const selectPreset = (id: string) => {
    const preset = getPresetWithConnections(id);
    if (!preset) {
      const next = editingID ? draft : { ...emptyDraft(), id: generatedProviderID(providers) };
      setSelectedPreset(""); setGeneratedPresetID(editingID ? "" : next.id); setGeneratedPresetName(""); setDraft(next); setAdvancedOpen(true); resetOAuth(); setError(""); return;
    }
    const result = applyPresetSelection(draft, preset, { editing: Boolean(editingID), previousGeneratedId: generatedPresetID, previousGeneratedName: generatedPresetName });
    if (editingID) {
      result.draft.secret = draft.secret;
      result.draft.remove_secret = draft.remove_secret;
      result.draft.enabled = draft.enabled;
    }
    if (!editingID && result.generatedId) {
      result.draft.id = availablePresetID(result.generatedId, providers);
    }
    setSelectedPreset(id);
    setGeneratedPresetID(result.generatedId ? result.draft.id : "");
    setGeneratedPresetName(result.generatedName);
    setDraft(result.draft);
    setAdvancedOpen(false); resetOAuth(); setError("");
  };

  const updateDraft = <K extends keyof ProviderDraft>(key: K, value: ProviderDraft[K]) => {
    setSelectedPreset((current) => nextPresetSelection(current, String(key)));
    if (key === "id") setGeneratedPresetID("");
    if (key === "name") setGeneratedPresetName("");
    if (key === "auth_mode") {
      const mode = value as ProviderDraft["auth_mode"];
      setDraft((current) => ({ ...current, auth_mode: mode, oauth_provider: mode === "oauth" ? current.oauth_provider : "", protocol: mode === "oauth" ? "adapter" : current.protocol === "adapter" ? "openai" : current.protocol, endpoint: mode === "oauth" ? "" : current.endpoint }));
      resetOAuth(); return;
    }
    setDraft((current) => ({ ...current, [key]: value }));
    if (key === "oauth_provider") resetOAuth();
  };

  const updatePresetProtocol = (protocol: ProviderDraft["protocol"]) => {
    const preset = selectedPreset ? getPresetWithConnections(selectedPreset) : undefined;
    const connection = preset ? connectionForProtocol(preset, protocol) : undefined;
    if (!preset || !connection) return;
    setDraft((current) => ({ ...current, protocol: connection.protocol, endpoint: preset.managedEndpoint ? "" : connection.endpoint }));
  };

  const saveDraft = async (requestedEnabled: boolean) => {
    const id = draft.id.trim();
    if (!id || !draft.name.trim()) { setError("请填写供应商 ID 和名称。"); return; }
    if (!editingID && providers.some((provider) => provider.id === id)) { setError("这个供应商 ID 已存在，请修改高级区中的 ID，避免覆盖已有配置。"); return; }
    const existing = editingID ? providers.find((provider) => provider.id === editingID) : undefined;
    if (draft.auth_mode === "api_key" && requestedEnabled && !draft.secret && !existing?.has_secret) { setError("启用 API Key 上游前必须填写厂商专属 Key。"); return; }
    if (draft.remove_secret && requestedEnabled) { setError("删除已保存密钥后不能同时启用，请先仅保存为停用状态。"); return; }
    const oauthProvider = providerFor(draft.oauth_provider);
    if (draft.auth_mode === "oauth" && requestedEnabled && (!oauthProvider || !accountUsable(accounts[oauthProvider]))) { setError("OAuth 账户状态未确认可用，不能启用供应商。"); return; }
    setBusy(true); setError("");
    try {
      const input: ProviderInput = { id, name: draft.name.trim(), kind: draft.kind, auth_mode: draft.auth_mode, protocol: draft.protocol, endpoint: draft.endpoint.trim(), enabled: requestedEnabled, oauth_provider: draft.oauth_provider, ...(draft.secret ? { secret: draft.secret } : {}), ...(draft.remove_secret ? { remove_secret: true } : {}) };
      await client.saveProvider(id, input); setDraft((current) => ({ ...current, id, enabled: requestedEnabled, secret: "", remove_secret: false })); await onChange(); setEditingID(id); setToolsProviderID(id);
    } catch (saveError) { setError(displayError(saveError)); } finally { setBusy(false); }
  };
  const save = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); void saveDraft(draft.enabled); };

  const disable = async (provider: Provider) => {
    setBusy(true); setError("");
    try { await client.saveProvider(provider.id, { id: provider.id, name: provider.name, kind: provider.kind, auth_mode: provider.auth_mode, protocol: provider.protocol, endpoint: provider.endpoint, enabled: false, oauth_provider: provider.oauth_provider }); await onChange(); }
    catch (disableError) { setError(displayError(disableError)); } finally { setBusy(false); }
  };
  const remove = async (provider: Provider) => {
    if (!window.confirm(`确认删除供应商“${provider.name}”吗？`)) return;
    setBusy(true); setError("");
    try { await client.deleteProvider(provider.id); if (editingID === provider.id) beginCreate(); await onChange(); }
    catch (deleteError) { setError(displayError(deleteError)); } finally { setBusy(false); }
  };

  const startOAuth = async () => {
    const provider = providerFor(draft.oauth_provider);
    if (!provider) { setError("请选择受支持的 OAuth 提供商。"); return; }
    if (!settings?.adapter_configured || !(settings.supported_oauth ?? []).includes(provider)) { setError("适配器尚未配置，当前无法开始授权。"); return; }
    if ((accounts[provider]?.account_count ?? 0) > 0) { setError("本机已有授权，请先断开后再重新登录。"); return; }
    setOAuthBusy(true); setError(""); setOAuthMessage("");
    try {
      const result = await client.startOAuth(provider); setOAuthState(result.state); setOAuthURL(result.url); setOAuthUserCode(result.user_code ?? ""); setOAuthExpiresIn(result.expires_in); setOAuthDeadline(Date.now() + Math.min((result.expires_in ?? 600) * 1000, 10 * 60 * 1000)); setOAuthStatus({ provider: result.provider, state: result.state, used: false, status: "pending" }); setOAuthMessage("授权已开始，等待状态确认。");
    } catch (oauthError) { setError(displayError(oauthError)); } finally { setOAuthBusy(false); }
  };
  const submitCallback = async () => {
    if (!oauthState || !callbackURL.trim()) { setError("请粘贴官方回调地址后再提交。"); return; }
    setOAuthBusy(true); setError("");
    try { await client.oauthCallback({ provider: draft.oauth_provider, state: oauthState, callback_url: callbackURL.trim() }); setCallbackURL(""); setOAuthMessage("回调已提交，等待授权状态确认。"); }
    catch (callbackError) { setError(displayError(callbackError)); } finally { setOAuthBusy(false); }
  };
  const refreshAccount = async (provider: OAuthProvider) => {
    setAccountBusy(provider); setAccountError("");
    try { await client.refreshOAuthAccount(provider); const updated = await readAccount(provider); if (!accountUsable(updated)) throw new Error("刷新后账户状态未确认可用"); await onChange(); }
    catch (refreshError) { setAccountError(displayError(refreshError)); } finally { setAccountBusy(""); }
  };
  const disconnectAccount = async (provider: OAuthProvider) => {
    if (!window.confirm("确认断开本机授权吗？这会停用关联上游并移除本机授权文件，不会在供应商侧注销账户。")) return;
    setAccountBusy(provider); setAccountError("");
    try { await client.disconnectOAuthAccount(provider); const updated = await readAccount(provider); if (updated.status !== "missing" || updated.account_count !== 0) throw new Error("断开后账户状态未确认"); setDraft((current) => ({ ...current, enabled: false })); await onChange(); }
    catch (disconnectError) { setAccountError(displayError(disconnectError)); } finally { setAccountBusy(""); }
  };

  const supportedOAuth = (provider: OAuthProvider) => Boolean(settings?.adapter_configured && (settings.supported_oauth ?? []).includes(provider));
  const currentAccountProvider = providerFor(draft.oauth_provider);
  const currentAccount = currentAccountProvider ? accounts[currentAccountProvider] : undefined;
  const oauthReady = draft.auth_mode !== "oauth" || accountUsable(currentAccount);
  const selectedPresetDetails: ProviderPresetWithConnections | undefined = selectedPreset ? getPresetWithConnections(selectedPreset) : undefined;
  const presetConnection = selectedPresetDetails ? connectionForProtocol(selectedPresetDetails, draft.protocol) : undefined;
  const protocolOptions = selectedPresetDetails ? protocolChoices(selectedPresetDetails) : ["openai", "responses", "anthropic", "adapter"] as const;
  const protocolLabel = (protocol: string) => protocol === "openai" ? "OpenAI 兼容" : protocol === "responses" ? "Responses" : protocol === "anthropic" ? "Anthropic" : "订阅适配（自动）";

  const toolsProvider = toolsProviderID ? providers.find((provider) => provider.id === toolsProviderID) : undefined;
  return <section className="page provider-page" aria-labelledby="provider-page-title">
    <div className="page-heading"><div><p className="eyebrow">上游账号</p><h1 id="provider-page-title">管理上游连接</h1><p className="muted">先选已核对的预设，再填写厂商专属凭据；端点等技术字段收在高级区。</p></div><button className="button button-primary" type="button" disabled={busy} onClick={beginCreate}>添加上游</button></div>
    {error && <p className="status status-error" role="alert">{error}</p>}
    <div className="panel table-scroll"><p className="field-hint provider-status-note">状态分别表示：已保存 Key / OAuth 账户、上游启用状态、以及最近一次连通测试结果；三者不互相替代。</p><table className="data-table"><caption>已配置上游</caption><thead><tr><th>名称</th><th>接入方式</th><th>协议</th><th>状态</th><th>密钥/账户</th><th>操作</th></tr></thead><tbody>{providers.length === 0 ? <tr><td colSpan={6} className="empty-state">暂无上游配置，先添加一个已核对的连接。</td></tr> : providers.map((provider) => { const accountProvider = providerFor(provider.oauth_provider); const account = accountProvider ? accounts[accountProvider] : undefined; return <tr key={provider.id}><td><strong>{provider.name}</strong><small className="breakable-code">{provider.id}</small></td><td>{provider.auth_mode === "oauth" ? "订阅登录" : provider.auth_mode === "api_key" ? "API Key" : "无需凭据"}</td><td>{protocolLabel(provider.protocol)}</td><td><span className={`status-dot ${provider.enabled ? "status-dot-on" : "status-dot-off"}`}>{provider.enabled ? "已启用" : "已停用"}</span></td><td>{accountProvider ? `${ACCOUNT_LABELS[account?.status ?? "unknown"]} · ${account?.account_count ?? 0} 个` : provider.has_secret ? "已保存（不显示）" : "未配置"}</td><td className="actions"><button className="button button-small" type="button" disabled={busy} onClick={() => { beginEdit(provider); setToolsProviderID(provider.id); }}>编辑</button><button className="button button-small" type="button" disabled={busy} onClick={() => setToolsProviderID(provider.id)}>模型与连通测试</button>{provider.enabled && <button className="button button-small" type="button" disabled={busy} onClick={() => void disable(provider)}>停用</button>}<button className="button button-small button-danger" type="button" disabled={busy} onClick={() => void remove(provider)}>删除</button></td></tr>; })}</tbody></table></div>
    {toolsProvider ? <><p className="field-hint provider-tools-note">测试的是已保存配置；修改后请先保存，再重新发现模型或测试连通性。</p><ProviderTools client={client} provider={toolsProvider} /></> : !editingID && <p className="field-hint provider-tools-empty">新建上游尚未保存；保存后才能发现模型或发起连通测试。</p>}
    <form className="panel form-panel" onSubmit={save}>
      <div className="panel-heading"><div><h2>{editingID ? "编辑上游" : "新增上游"}</h2><p className="muted">默认只需选择预设、确认名称和协议，再填写 API Key 或完成官方登录。</p></div></div>
      <div className="form-grid">
        <label className="field field-wide">预设<select value={selectedPreset} disabled={busy} onChange={(event) => selectPreset(event.target.value)}><option value="">自定义配置</option>{PROVIDER_PRESETS.map((preset) => <option key={preset.id} value={preset.id}>{preset.label}</option>)}</select><span className="field-hint">预设会自动带出官网核对过的类型、认证、协议和端点；自定义配置请在高级区补全技术字段。编辑时切换预设不会清除已有 Key。</span>{selectedPresetDetails && <span className="field-hint preset-meta"><strong>{sourceLabel(selectedPresetDetails.configSource)}</strong>{selectedPresetDetails.verifiedAt && <> · 核实日期 {selectedPresetDetails.verifiedAt}</>}{selectedPresetDetails.source && <> · <a href={selectedPresetDetails.source} target="_blank" rel="noreferrer">官方资料</a></>}{presetConnection?.source && presetConnection.source !== selectedPresetDetails.source && <> · <a href={presetConnection.source} target="_blank" rel="noreferrer">协议资料</a></>}{<br />}{selectedPresetDetails.note}</span>}</label>
        <label className="field">名称<input value={draft.name} disabled={busy} onChange={(event) => updateDraft("name", event.target.value)} placeholder="例如 DeepSeek API" required /><span className="field-hint">用于管理页识别，不会改变上游模型 ID。</span></label>
        <label className="field">协议<select value={draft.protocol} disabled={busy} onChange={(event) => selectedPresetDetails ? updatePresetProtocol(event.target.value as ProviderDraft["protocol"]) : updateDraft("protocol", event.target.value as ProviderDraft["protocol"])}>{protocolOptions.map((protocol) => <option key={protocol} value={protocol}>{protocolLabel(protocol)}</option>)}</select><span className="field-hint">预设协议来自对应官方资料；适配器允许三种已支持协议。</span></label>
        {draft.auth_mode === "api_key" && <label className="field field-wide">厂商专属 API Key<input type="password" autoComplete="new-password" disabled={busy} value={draft.secret} onChange={(event) => updateDraft("secret", event.target.value)} placeholder={editingID ? "已保存则留空以保留现有 Key" : "粘贴厂商专属 Key"} /><span className="field-hint">只填写该厂商专属 API Key。已保存时留空会保留原值；这里不能填写订阅 token 或 adapter 端点。</span></label>}
      </div>
      <details className="advanced-section" open={advancedOpen} onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}><summary>高级：ID、类型、端点、认证与启用状态</summary><div className="form-grid">
        <label className="field">公开 ID<input value={draft.id} readOnly={Boolean(editingID)} onChange={(event) => updateDraft("id", event.target.value)} required /><span className="field-hint">新建预设会自动避免重复 ID；自定义 ID 只能使用安全字符。编辑时只读，不会改变已有模型引用。</span></label>
        <label className="field">类型<select value={draft.kind} disabled={busy} onChange={(event) => updateDraft("kind", event.target.value as ProviderDraft["kind"])}><option value="api">API</option><option value="subscription">订阅</option><option value="local">本地</option></select><span className="field-hint">用于权限和本地-only 路由判断；预设会自动选择。</span></label>
        <label className="field field-wide">端点<input type="url" value={draft.endpoint} disabled={busy || (draft.auth_mode === "oauth" && draft.protocol === "adapter")} onChange={(event) => updateDraft("endpoint", event.target.value)} placeholder={draft.auth_mode === "oauth" && draft.protocol === "adapter" ? "自动配置，无需填写" : "https://..."} /><span className="field-hint">仅填写已核对的 HTTPS/HTTP 基础端点；OAuth 适配器端点由服务端配置，不能手填。</span></label>
        <label className="field">认证方式<select value={draft.auth_mode} disabled={busy} onChange={(event) => updateDraft("auth_mode", event.target.value as ProviderDraft["auth_mode"])}><option value="api_key">API Key</option><option value="oauth">OAuth / 官方设备授权</option><option value="none">无需凭据</option></select><span className="field-hint">API Key 只使用厂商 Key；OAuth 通过官方登录；无需凭据只适用于受信任的本地端点。</span></label>
        {draft.auth_mode === "oauth" && <label className="field">OAuth 提供商<select value={draft.oauth_provider} disabled={busy} onChange={(event) => updateDraft("oauth_provider", event.target.value)}><option value="">选择提供商</option><option value="codex">Codex</option><option value="kimi">Kimi</option></select><span className="field-hint">OAuth 回调 URL 必须来自官方授权流程；不要粘贴 token 或账户文件内容。</span></label>}
        {editingID && draft.auth_mode === "api_key" && <label className="field checkbox-field"><input type="checkbox" disabled={busy} checked={draft.remove_secret} onChange={(event) => updateDraft("remove_secret", event.target.checked)} />删除已保存密钥<span className="field-hint">删除后只能先停用保存，不能与“保存并启用”同时提交。</span></label>}
      </div></details>
      {draft.auth_mode === "oauth" && <div className="oauth-box">
        <div className="oauth-heading"><div><h3>订阅账户</h3><p className="muted">仅显示脱敏状态和账户数量，不读取或展示 token、邮箱或账户文件名。</p></div>{currentAccountProvider && <div className="actions"><button className="button button-small" type="button" disabled={busy || accountBusy === currentAccountProvider || !supportedOAuth(currentAccountProvider)} onClick={() => void refreshAccount(currentAccountProvider)}>{accountBusy === currentAccountProvider ? "处理中…" : "刷新授权"}</button><button className="button button-small button-danger" type="button" disabled={busy || accountBusy === currentAccountProvider || !currentAccount || currentAccount.account_count === 0} onClick={() => void disconnectAccount(currentAccountProvider)}>断开本机授权</button></div>}</div>
        {currentAccountProvider && <p className="status status-info">状态：{ACCOUNT_LABELS[currentAccount?.status ?? "unknown"]} · 账户数：{currentAccount?.account_count ?? 0}{currentAccount?.model_prefix ? ` · 模型前缀：${currentAccount.model_prefix}` : ""}</p>}
        {accountError && <p className="status status-error" role="alert">{accountError}</p>}
        {currentAccountProvider && !supportedOAuth(currentAccountProvider) && <p className="muted">适配器尚未配置或未声明此 OAuth 提供商，暂不能开始登录。</p>}
        <div className="oauth-heading"><div><h3>首次官方授权</h3><p className="muted">已有账户时无需再次登录；状态未知、过期或异常时不能启用上游。</p></div><button className="button" type="button" disabled={busy || oauthBusy || !currentAccountProvider || !supportedOAuth(currentAccountProvider) || (currentAccount?.account_count ?? 0) > 0} onClick={() => void startOAuth()}>{oauthBusy ? "处理中…" : "开始官方授权"}</button></div>
        {oauthURL && <p><a href={oauthURL} target="_blank" rel="noreferrer">打开官方授权页面</a></p>}
        {oauthUserCode && <p className="device-code">设备码：<code>{oauthUserCode}</code>{oauthExpiresIn ? `（有效期约 ${oauthExpiresIn} 秒）` : ""}</p>}
        {oauthState && <p className="status status-info">当前状态：{oauthStatus?.status ?? "pending"}{oauthMessage ? ` · ${oauthMessage}` : ""}<button className="button button-small" type="button" disabled={busy || oauthBusy} onClick={() => void inspectOAuth().catch((statusError) => setOAuthMessage(displayError(statusError)))}>手动检查</button></p>}
        {draft.oauth_provider === "codex" && oauthState && <div className="field field-wide"><label htmlFor="callback-url">粘贴官方回调地址（用于完成授权）</label><input id="callback-url" type="url" disabled={busy || oauthBusy} value={callbackURL} onChange={(event) => setCallbackURL(event.target.value)} placeholder="http://localhost:1455/auth/callback?..." /><span className="field-hint">仅粘贴官方回调地址；不要手工构造 URL 或填写 access token。</span><button className="button button-small" type="button" disabled={busy || oauthBusy} onClick={() => void submitCallback()}>提交回调</button></div>}
      </div>}
      <div className="actions form-actions"><button className="button button-primary" type="button" disabled={busy || !oauthReady} onClick={() => void saveDraft(true)}>{busy ? "保存中…" : "保存并启用"}</button><button className="button" type="button" disabled={busy} onClick={() => void saveDraft(false)}>仅保存，暂不启用</button>{editingID && <button className="button" type="button" disabled={busy} onClick={beginCreate}>取消编辑</button>}{draft.auth_mode === "oauth" && !oauthReady && <span className="muted">先完成并确认有效账户状态，才能保存并启用。</span>}</div>
    </form>
  </section>;

}
