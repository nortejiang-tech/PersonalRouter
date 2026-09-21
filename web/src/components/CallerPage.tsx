import { useEffect, useRef, useState, type FormEvent } from "react";
import type { AdminClient, Caller, CallerCreateInput, CallerUpdateInput, Model, Provider, Settings } from "../api";
import { copyText } from "../lib/clipboard";

interface CallerPageProps {
  client: AdminClient;
  providers: Provider[];
  models: Model[];
  callers: Caller[];
  settings: Settings;
  onChange: () => Promise<void>;
}

type CallerDraft = Omit<CallerCreateInput, "id"> & { id: string };

function emptyDraft(allowedModels: string[] = []): CallerDraft {
  return { id: "", name: "", allowed_models: allowedModels, access_scope: "lan", local_only: false, enabled: true, rpm: 0, max_concurrency: 0, daily_token_limit: 0 };
}

function draftFromCaller(caller: Caller): CallerDraft {
  return { id: caller.id, name: caller.name, allowed_models: [...caller.allowed_models], access_scope: caller.access_scope, local_only: caller.local_only, enabled: caller.enabled, rpm: caller.rpm, max_concurrency: caller.max_concurrency, daily_token_limit: caller.daily_token_limit };
}

function displayError(error: unknown): string { return error instanceof Error ? error.message : "保存失败，请稍后重试"; }
function providerForModel(model: Model, providers: Provider[]): Provider | undefined { return providers.find((provider) => provider.id === model.provider_id); }
function isLocalModel(model: Model, providers: Provider[]): boolean { return providerForModel(model, providers)?.kind === "local"; }
function readyModelIDs(models: Model[], providers: Provider[], localOnly: boolean): string[] { return models.filter((model) => model.enabled && providerForModel(model, providers)?.enabled && (!localOnly || isLocalModel(model, providers))).map((model) => model.id); }

function modelReasons(model: Model | undefined, providers: Provider[], localOnly: boolean): string[] {
  if (!model) return ["模型不存在"];
  const provider = providerForModel(model, providers);
  const reasons: string[] = [];
  if (!provider) reasons.push("所属上游不存在");
  else if (!provider.enabled) reasons.push("上游已停用");
  if (!model.enabled) reasons.push("模型已停用");
  if (localOnly && provider?.kind !== "local") reasons.push("仅本地限制");
  return reasons;
}

function modelStatus(model: Model | undefined, providers: Provider[], localOnly: boolean): string {
  const reasons = modelReasons(model, providers, localOnly);
  return reasons.length === 0 ? "就绪" : reasons.join(" · ");
}

function scopeLabel(scope: CallerDraft["access_scope"]): string { return scope === "lan" ? "LAN" : scope === "public" ? "公网" : "LAN + 公网"; }

function CallerAccessCard({ draft, settings, models, providers }: { draft: CallerDraft; settings: Settings; models: Model[]; providers: Provider[] }) {
  const [message, setMessage] = useState("");
  const copy = async (value: string, label: string) => setMessage((await copyText(value)) ? `${label}已复制` : `${label}复制失败，请手动选择复制`);
  const allowed = draft.allowed_models.map((id) => models.find((model) => model.id === id));
  const urls = [["LAN Base URL", settings.lan_base_url], ["公网 Base URL", settings.public_base_url]] as const;
  return <section className="panel access-card" aria-labelledby="caller-access-title">
    <div className="panel-heading"><div><h3 id="caller-access-title">接入信息</h3><p className="muted">使用完整公开模型 ID。一个调用方通常对应一台电脑；同一个 Key 可以同时复用于 ZCode、Codex 或其他工具。</p></div></div>
    <p className="field-hint">入口范围：{scopeLabel(draft.access_scope)}。协议必须与上游支持的协议匹配。</p>
    <div className="form-grid">{urls.map(([label, value]) => <div className="field" key={label}><span>{label}</span><code className="copy-value">{value ?? "服务端未返回"}</code><button type="button" className="button button-small" disabled={!value} onClick={() => value && void copy(value, label)}>复制地址</button></div>)}</div>
    <div className="field field-wide"><span>当前表单选择（保存后生效）</span>{allowed.length === 0 ? <p className="field-hint">未选择模型；保存后此调用方没有模型访问权限。</p> : <div className="model-access-list">{allowed.map((model, index) => { const id = draft.allowed_models[index]; return <div className="actions" key={id}><code className="copy-value">{id}</code><span className="field-hint">{model ? `${model.protocols.join("、")} · 状态：${modelStatus(model, providers, draft.local_only)}` : "状态：模型不存在"}</span><button type="button" className="button button-small" onClick={() => void copy(id, "模型 ID")}>复制 ID</button></div>; })}</div>}</div>
    {message && <p className="status status-info" role="status">{message}</p>}
  </section>;
}

export function CallerPage({ client, providers, models, callers, settings, onChange }: CallerPageProps) {
  const [editingID, setEditingID] = useState<string | null>(null);
  const [draft, setDraft] = useState<CallerDraft>(() => emptyDraft(readyModelIDs(models, providers, false)));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [newKey, setNewKey] = useState("");
  const [keyMessage, setKeyMessage] = useState("");
  const [keyVisible, setKeyVisible] = useState(false);
  const [keyPanelOpen, setKeyPanelOpen] = useState(false);
  const [keyCallerID, setKeyCallerID] = useState<string | null>(null);
  const [keyCallerName, setKeyCallerName] = useState("");
  const [keyAvailable, setKeyAvailable] = useState(false);
  const [keyBusy, setKeyBusy] = useState(false);
  const [legacyKey, setLegacyKey] = useState("");
  const [keyError, setKeyError] = useState("");
  const keyGeneration = useRef(0);
  const keyCallerRef = useRef<string | null>(null);

  const clearKeyPanel = () => {
    keyGeneration.current += 1;
    keyCallerRef.current = null;
    setKeyPanelOpen(false);
    setKeyCallerID(null);
    setKeyCallerName("");
    setKeyAvailable(false);
    setKeyBusy(false);
    setLegacyKey("");
    setKeyError("");
    setNewKey("");
    setKeyMessage("");
    setKeyVisible(false);
  };

  useEffect(() => () => {
    keyGeneration.current += 1;
    keyCallerRef.current = null;
  }, []);

  const beginCreate = () => { if (busy) return; clearKeyPanel(); setEditingID(null); setDraft(emptyDraft(readyModelIDs(models, providers, false))); setError(""); };
  const beginEdit = (caller: Caller) => { if (busy) return; clearKeyPanel(); setEditingID(caller.id); setDraft(draftFromCaller(caller)); setError(""); };
  const toggleModel = (modelID: string) => setDraft((current) => ({ ...current, allowed_models: current.allowed_models.includes(modelID) ? current.allowed_models.filter((id) => id !== modelID) : [...current.allowed_models, modelID] }));
  const setLocalOnly = (value: boolean) => setDraft((current) => ({ ...current, local_only: value }));

  const validateDraft = (): string => {
    if (!draft.name.trim()) return "请填写调用方名称。";
    if (draft.enabled && draft.allowed_models.length === 0) return "启用调用方前请先选择至少一个模型；如暂不授权，请先停用调用方。";
    if (draft.local_only && draft.allowed_models.some((id) => modelReasons(models.find((model) => model.id === id), providers, true).includes("仅本地限制"))) return "仅本地调用方不能授权云端模型，请取消仍标记为仅本地限制的模型。";
    return "";
  };

  const createCaller = async () => {
    const result = await client.createCaller({ name: draft.name.trim(), allowed_models: draft.allowed_models, access_scope: draft.access_scope, local_only: draft.local_only, enabled: draft.enabled, rpm: draft.rpm, max_concurrency: draft.max_concurrency, daily_token_limit: draft.daily_token_limit });
    setDraft((current) => ({ ...current, id: result.caller.id }));
    setEditingID(result.caller.id);
    showKeyFromValue(result.caller, result.key, "新 Key 已生成并暂存在当前页面内；同一个 Key 可供这台电脑上的多个工具共用。关闭后仍可用“查看 Key”重新获取。");
  };

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const validationError = validateDraft();
    if (validationError) { setError(validationError); return; }
    setBusy(true); setError("");
    try {
      if (editingID) {
        const input: CallerUpdateInput = { name: draft.name.trim(), allowed_models: draft.allowed_models, access_scope: draft.access_scope, local_only: draft.local_only, enabled: draft.enabled, rpm: draft.rpm, max_concurrency: draft.max_concurrency, daily_token_limit: draft.daily_token_limit };
        await client.updateCaller(editingID, input);
      } else await createCaller();
      await onChange();
    } catch (saveError) { setError(displayError(saveError)); } finally { setBusy(false); }
  };

  const disable = async (caller: Caller) => { setBusy(true); setError(""); try { await client.updateCaller(caller.id, { enabled: false }); await onChange(); } catch (disableError) { setError(displayError(disableError)); } finally { setBusy(false); } };
  const remove = async (caller: Caller) => {
    if (!window.confirm(`确认删除调用方“${caller.name}”吗？历史调用记录会保留。`)) return;
    setBusy(true); setError("");
    clearKeyPanel();
    try { await client.deleteCaller(caller.id); if (editingID === caller.id) { setEditingID(null); setDraft(emptyDraft(readyModelIDs(models, providers, false))); } await onChange(); } catch (deleteError) { setError(displayError(deleteError)); } finally { setBusy(false); }
  };
  const rotate = async (caller: Caller) => {
    if (!window.confirm("旋转 Key 会立即使当前客户端凭据失效。所有共享这个 Key 的 ZCode、Codex 或其他工具都必须更新。确认继续吗？")) return;
    setBusy(true); setError("");
    clearKeyPanel();
    try { const result = await client.rotateCaller(caller.id); showKeyFromValue(caller, result.key, "旧 Key 已失效。请更新所有共用此 Key 的 ZCode、Codex 或其他工具；新 Key 也可在此页面反复查看。"); } catch (rotateError) { setError(displayError(rotateError)); } finally { setBusy(false); }
  };
  const showKeyFromValue = (caller: Caller, key: string, message: string) => {
    keyGeneration.current += 1;
    keyCallerRef.current = caller.id;
    setKeyCallerID(caller.id); setKeyCallerName(caller.name); setKeyPanelOpen(true); setKeyAvailable(true); setKeyBusy(false); setKeyError(""); setLegacyKey(""); setNewKey(key); setKeyVisible(true); setKeyMessage(message);
  };

  const applyKeyReveal = (caller: Caller, generation: number, result: Awaited<ReturnType<AdminClient["revealCallerKey"]>>) => {
    if (keyGeneration.current !== generation || keyCallerRef.current !== caller.id) return;
    setKeyBusy(false); setKeyError("");
    if (result.available && typeof result.key === "string" && result.key.length > 0) {
      setKeyAvailable(true); setNewKey(result.key); setKeyVisible(true); setKeyMessage("这是当前保存的 Key；同一个 Key 可以同时复用于 ZCode、Codex 或其他工具。关闭后仍可用“查看 Key”重新获取。");
    } else {
      setKeyAvailable(false); setNewKey(""); setKeyVisible(false); setKeyMessage("旧版只保留了摘要；让已配置工具正常请求一次（刷新模型列表也可）后再查看，无需换 Key");
    }
  };

  const revealKey = async (caller: Caller) => {
    if (busy) return;
    const generation = keyGeneration.current + 1;
    keyGeneration.current = generation; keyCallerRef.current = caller.id;
    setKeyCallerID(caller.id); setKeyCallerName(caller.name); setKeyPanelOpen(true); setKeyAvailable(false); setNewKey(""); setKeyVisible(false); setKeyMessage(""); setKeyError(""); setLegacyKey(""); setKeyBusy(true);
    try { applyKeyReveal(caller, generation, await client.revealCallerKey(caller.id)); } catch (revealError) {
      if (keyGeneration.current === generation && keyCallerRef.current === caller.id) { setKeyBusy(false); setKeyError(displayError(revealError)); }
    }
  };

  const saveLegacyKey = async () => {
    const callerID = keyCallerID;
    const value = legacyKey;
    if (!callerID || !value) { setKeyError("请输入原 Key 后再保存。"); return; }
    const caller = callers.find((item) => item.id === callerID);
    if (!caller) { setKeyError("调用方已不存在，请刷新后重试。"); return; }
    const generation = keyGeneration.current;
    setKeyBusy(true); setKeyError("");
    try {
      await client.saveCallerKey(callerID, value);
      if (keyGeneration.current !== generation || keyCallerRef.current !== callerID) return;
      setLegacyKey(""); applyKeyReveal(caller, generation, await client.revealCallerKey(callerID));
    } catch (saveError) {
      if (keyGeneration.current === generation && keyCallerRef.current === callerID) { setKeyBusy(false); setKeyError(displayError(saveError)); }
    }
  };

  const copyKey = async () => { if (newKey && keyAvailable) setKeyMessage((await copyText(newKey)) ? "已复制到剪贴板。" : "浏览器未允许自动复制，请手动选择并复制。"); };

  const knownModelIDs = new Set(models.map((model) => model.id));
  const unknownModelIDs = draft.allowed_models.filter((id) => !knownModelIDs.has(id));

  return <section className="page caller-page" aria-labelledby="caller-page-title">
    <div className="page-heading"><div><p className="eyebrow">调用方与 Key</p><h1 id="caller-page-title">控制访问策略</h1><p className="muted">一个调用方通常代表一台电脑。同一个 Key 可以复用于 ZCode、Codex 或其他工具，共享相同权限、RPM、并发、预算和汇总用量。</p></div><button className="button button-primary" type="button" onClick={beginCreate} disabled={busy}>添加调用方</button></div>
    {error && <p className="status status-error" role="alert">{error}</p>}
    {keyPanelOpen && keyCallerID && <div className="panel credential-panel" role="status"><div><h2>调用方 Key：{keyCallerName}</h2><p className="muted">调用方 ID：{keyCallerID}。明文 Key 只保留在当前页面，关闭后会清除；需要时可随时再次查看。</p>{keyBusy && <p className="status status-info">读取中…</p>}{keyAvailable && newKey && <>{keyVisible ? <code className="credential-value">{newKey}</code> : <code className="credential-value">••••••••••••••••</code>}{keyMessage && <p className="status status-info">{keyMessage}</p>}</>}{!keyBusy && !keyAvailable && <><p className="status status-info">{keyMessage}</p><details><summary>手动保存原 Key</summary><p className="field-hint">只在你仍持有旧 Key 时使用；保存成功后即可反复查看，无需旋转。</p><div className="actions"><input aria-label="原 Key" type="password" value={legacyKey} onChange={(event) => setLegacyKey(event.target.value)} placeholder="粘贴原 Key" disabled={keyBusy} /><button className="button button-small" type="button" onClick={() => void saveLegacyKey()} disabled={keyBusy || !legacyKey}>保存原 Key</button></div></details></>}{keyError && <p className="status status-error" role="alert">{keyError}</p>}</div><div className="actions">{keyAvailable && newKey && <><button className="button button-primary" type="button" onClick={() => setKeyVisible((visible) => !visible)}>{keyVisible ? "隐藏 Key" : "显示 Key"}</button><button className="button" type="button" onClick={() => void copyKey()}>复制 Key</button></>}<button className="button" type="button" onClick={clearKeyPanel}>关闭并清除</button></div></div>}
    <div className="panel table-scroll"><table className="data-table"><caption>已配置调用方</caption><thead><tr><th>名称</th><th>授权模型</th><th>入口</th><th>状态</th><th>限制</th><th>操作</th></tr></thead><tbody>{callers.length === 0 ? <tr><td colSpan={6} className="empty-state">暂无调用方。创建一个 Key 后再接入工具。</td></tr> : callers.map((caller) => <tr key={caller.id}><td><strong>{caller.name}</strong></td><td>{caller.allowed_models.length || "无"}</td><td>{scopeLabel(caller.access_scope)}{caller.local_only ? " · 仅本地" : ""}</td><td><span className={`status-dot ${caller.enabled ? "status-dot-on" : "status-dot-off"}`}>{caller.enabled ? "已启用" : "已停用"}</span></td><td>{caller.rpm ? `${caller.rpm} RPM` : "RPM 不限"} · {caller.daily_token_limit ? `${caller.daily_token_limit} token/日` : "日限额不限"}</td><td className="actions"><button className="button button-small" type="button" onClick={() => beginEdit(caller)} disabled={busy}>编辑</button><button className="button button-small" type="button" onClick={() => void revealKey(caller)} disabled={busy || keyBusy}>查看 Key</button>{caller.enabled && <button className="button button-small" type="button" onClick={() => void disable(caller)} disabled={busy}>停用</button>}<button className="button button-small" type="button" onClick={() => void rotate(caller)} disabled={busy}>旋转 Key</button><button className="button button-small button-danger" type="button" onClick={() => void remove(caller)} disabled={busy}>删除</button></td></tr>)}</tbody></table></div>
    <form className="panel form-panel" onSubmit={save}>
      <div className="panel-heading"><div><h2>{editingID ? "编辑调用方" : "新增调用方"}</h2>{editingID ? <p className="muted">内部 ID：{editingID}。改名不会改变关联或旧 Key。</p> : <p className="muted">先填写名称；新建时服务端会自动生成内部 ID。</p>}</div></div>
      <div className="form-grid">
        <label className="field field-wide">名称<input value={draft.name} onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))} placeholder="例如：家中电脑、公司电脑" required disabled={busy} /><span className="field-hint">用于识别这台电脑，不会改变内部 ID；同一 Key 可同时给多个工具使用。</span></label>
        <label className="field">入口范围<select value={draft.access_scope} onChange={(event) => setDraft((current) => ({ ...current, access_scope: event.target.value as CallerDraft["access_scope"] }))} disabled={busy}><option value="lan">LAN</option><option value="public">公网</option><option value="both">LAN + 公网</option></select><span className="field-hint">LAN 仅家庭网络；公网供已配置的公开入口；both 两者都允许。</span></label>
        <label className="field checkbox-field"><input type="checkbox" checked={draft.local_only} onChange={(event) => setLocalOnly(event.target.checked)} disabled={busy} />仅允许本地模型<span className="field-hint">勾选后只能授权本地上游模型。</span></label>
        <label className="field checkbox-field"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} disabled={busy} />启用调用方<span className="field-hint">启用且没有模型权限时不能保存；停用可保留空权限。</span></label>
        <fieldset className="field field-wide checkbox-group"><legend>模型权限</legend><p className="field-hint">只授权当前需要的完整公开模型 ID；新建默认选择当前就绪模型，不会自动加入未来新增模型。已选择 {draft.allowed_models.length} 个。</p>{editingID && draft.allowed_models.length === 0 && <p className="status status-error">当前调用方没有模型权限；启用前请先选择模型。</p>}{models.length === 0 ? <p className="muted">暂无模型，保存后仍无模型权限。</p> : models.map((model) => { const local = isLocalModel(model, providers); const selected = draft.allowed_models.includes(model.id); const disabled = busy || (draft.local_only && !local && !selected); return <label className={`checkbox-field ${disabled ? "field-disabled" : ""}`} key={model.id}><input type="checkbox" checked={selected} disabled={disabled} onChange={() => toggleModel(model.id)} /><span><strong>{model.id}</strong><small>{model.protocols.join("、")} · {modelStatus(model, providers, draft.local_only)}</small></span></label>; })}{unknownModelIDs.map((id) => <label className={`checkbox-field ${busy ? "field-disabled" : ""}`} key={`unknown-${id}`}><input type="checkbox" checked disabled={busy} onChange={() => toggleModel(id)} /><span><strong>{id}</strong><small>模型不存在 · 保留现有授权，取消勾选后才会移除</small></span></label>)}{unknownModelIDs.length > 0 && <p className="status status-error">当前已有授权列表包含未知模型 ID；默认保留，可取消勾选后再保存移除。</p>}</fieldset>
      </div>
      <details className="field-wide"><summary>高级限制</summary><div className="form-grid"><label className="field">RPM（0 不限）<input type="number" min="0" value={draft.rpm} onChange={(event) => setDraft((current) => ({ ...current, rpm: Math.max(0, Number(event.target.value) || 0) }))} disabled={busy} /><span className="field-hint">每分钟最多请求数；0 表示不设此项限制。</span></label><label className="field">最大并发（0 使用默认 2）<input type="number" min="0" value={draft.max_concurrency} onChange={(event) => setDraft((current) => ({ ...current, max_concurrency: Math.max(0, Number(event.target.value) || 0) }))} disabled={busy} /><span className="field-hint">同时处理的请求数；0 不是无限，而是使用服务端默认并发 2；多个工具共享这个上限。</span></label><label className="field field-wide">每日 token 限额（0 不限）<input type="number" min="0" value={draft.daily_token_limit} onChange={(event) => setDraft((current) => ({ ...current, daily_token_limit: Math.max(0, Number(event.target.value) || 0) }))} disabled={busy} /><span className="field-hint">按已知用量计算；未知用量时会保守拒绝后续请求。</span></label></div></details>
      <div className="actions form-actions"><button className="button button-primary" type="submit" disabled={busy}>{busy ? "保存中…" : editingID ? "保存策略" : "创建并生成 Key"}</button>{editingID && <button className="button" type="button" disabled={busy} onClick={beginCreate}>取消编辑</button>}</div>
    </form>
    <CallerAccessCard draft={draft} settings={settings} models={models} providers={providers} />
  </section>;
}
