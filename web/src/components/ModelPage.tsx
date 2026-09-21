import { useMemo, useState, type FormEvent } from "react";
import type { AdminClient, Caller, Model, ModelInput, Provider, Protocol } from "../api";
import { copyText } from "../lib/clipboard";
import { suggestModelID } from "../lib/model-catalog";
import { ProviderTools } from "./ProviderTools";

interface ModelPageProps { client: AdminClient; providers: Provider[]; models: Model[]; callers: Caller[]; onChange: () => Promise<void> }
type ModelDraft = Omit<ModelInput, "id" | "input_price" | "output_price"> & { id: string; input_price: string; output_price: string };

const emptyDraft = (provider?: Provider): ModelDraft => ({
  id: "", provider_id: provider?.id ?? "", upstream_model: "", name: "",
  protocols: [provider?.protocol === "adapter" ? "openai" : (provider?.protocol as Exclude<Protocol, "adapter">) || "openai"],
  input_images: false, enabled: true, input_price: "", output_price: "",
});

function draftFromModel(model: Model): ModelDraft { return { id: model.id, provider_id: model.provider_id, upstream_model: model.upstream_model, name: model.name, protocols: [...model.protocols], input_images: model.input_images, enabled: model.enabled, input_price: model.input_price === null ? "" : String(model.input_price), output_price: model.output_price === null ? "" : String(model.output_price) }; }
function displayError(error: unknown): string { return error instanceof Error ? error.message : "保存失败，请稍后重试"; }
function validPublicID(value: string): boolean { return value.length <= 128 && value.split("/").every((segment) => /^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(segment)) && !value.includes("..") && !value.endsWith("/"); }
function protocolLabel(protocol: string): string { return protocol === "openai" ? "OpenAI 兼容" : protocol === "responses" ? "Responses" : "Anthropic"; }

export function ModelPage({ client, providers, models, callers, onChange }: ModelPageProps) {
  void callers;
  const [editingID, setEditingID] = useState<string | null>(null);
  const [draft, setDraft] = useState<ModelDraft>(() => emptyDraft(providers[0]));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copyMessage, setCopyMessage] = useState("");
  const selectedProvider = useMemo(() => providers.find((provider) => provider.id === draft.provider_id), [draft.provider_id, providers]);
  const allowedProtocols = selectedProvider?.protocol === "adapter" ? ["openai", "responses", "anthropic"] as const : selectedProvider?.protocol ? [selectedProvider.protocol] as const : [];

  const beginCreate = (provider = providers[0]) => { if (busy) return; setEditingID(null); setDraft(emptyDraft(provider)); setError(""); };
  const beginEdit = (model: Model) => { if (busy) return; setEditingID(model.id); setDraft(draftFromModel(model)); setError(""); };
  const chooseProvider = (providerID: string) => {
    if (busy) return;
    const provider = providers.find((item) => item.id === providerID);
    if (provider) { beginCreate(provider); return; }
    setEditingID(null);
    setDraft(emptyDraft());
    setError("");
  };
  const chooseUpstream = (selection: { id: string; input_images: boolean } | null) => {
    if (busy) return;
    if (selection === null) {
      setDraft((current) => ({ ...current, upstream_model: "", id: editingID ? current.id : "", name: editingID ? current.name : "", input_images: editingID ? current.input_images : false }));
      setError("");
      return;
    }
    const existing = models.find((model) => model.provider_id === draft.provider_id && model.upstream_model === selection.id);
    if (existing && !editingID) { beginEdit(existing); return; }
    setDraft((current) => ({ ...current, id: editingID ? current.id : suggestModelID(current.provider_id, selection.id), upstream_model: selection.id, name: editingID ? current.name : selection.id, input_images: editingID ? current.input_images : selection.input_images }));
    setError("");
  };
  const toggleProtocol = (protocol: ModelDraft["protocols"][number]) => { if (!busy) setDraft((current) => ({ ...current, protocols: current.protocols.includes(protocol) ? current.protocols.filter((item) => item !== protocol) : [...current.protocols, protocol] })); };

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    if (!draft.provider_id || !draft.upstream_model.trim() || !draft.name.trim()) { setError("请先选择上游模型，并填写名称。"); return; }
    if (!validPublicID(draft.id.trim())) { setError("公开 ID 每段必须以字母或数字开头，例如 bailian/qwen3.8-max。"); return; }
    const duplicate = models.find((model) => model.id === draft.id.trim());
    if (!editingID && duplicate && (duplicate.provider_id !== draft.provider_id || duplicate.upstream_model !== draft.upstream_model)) { setError("该公开 ID 已被其他上游模型使用，请改用新的 ID。"); return; }
    if (draft.protocols.length === 0) { setError("至少选择一种协议。"); return; }
    const inputPrice = draft.input_price.trim() === "" ? null : Number(draft.input_price);
    const outputPrice = draft.output_price.trim() === "" ? null : Number(draft.output_price);
    if ((inputPrice !== null && (!Number.isFinite(inputPrice) || inputPrice < 0)) || (outputPrice !== null && (!Number.isFinite(outputPrice) || outputPrice < 0))) { setError("价格必须是非负数字；留空表示未知。"); return; }
    setBusy(true); setError("");
    try {
      const input: ModelInput = { id: draft.id.trim(), provider_id: draft.provider_id, upstream_model: draft.upstream_model.trim(), name: draft.name.trim(), protocols: draft.protocols, input_images: draft.input_images, enabled: draft.enabled, input_price: inputPrice, output_price: outputPrice };
      await client.saveModel(draft.id.trim(), input); await onChange(); setEditingID(draft.id.trim());
    } catch (saveError) { setError(displayError(saveError)); } finally { setBusy(false); }
  };
  const disable = async (model: Model) => { if (busy) return; setBusy(true); setError(""); try { const input: ModelInput = { id: model.id, provider_id: model.provider_id, upstream_model: model.upstream_model, name: model.name, protocols: model.protocols, input_images: model.input_images, enabled: false, input_price: model.input_price, output_price: model.output_price }; await client.saveModel(model.id, input); await onChange(); } catch (error) { setError(displayError(error)); } finally { setBusy(false); } };
  const remove = async (model: Model) => { if (busy || !window.confirm(`确认删除模型“${model.name}”吗？`)) return; setBusy(true); setError(""); try { await client.deleteModel(model.id); if (editingID === model.id) { setEditingID(null); setDraft(emptyDraft()); } await onChange(); } catch (error) { setError(displayError(error)); } finally { setBusy(false); } };
  const copyID = async (id: string) => { setCopyMessage((await copyText(id)) ? "完整公开 ID 已复制" : "复制失败，请手动选择公开 ID"); window.setTimeout(() => setCopyMessage(""), 2200); };

  return <section className="page model-page" aria-labelledby="model-page-title">
    <div className="page-heading"><div><p className="eyebrow">模型</p><h1 id="model-page-title">管理公开模型</h1><p className="muted">先选上游和真实模型，再保存完整公开 ID。ZCode 填写完整公开 ID，不是只填上游原名。</p></div><button className="button button-primary" type="button" onClick={() => beginCreate()} disabled={busy}>添加模型</button></div>
    {error && <p className="status status-error" role="alert">{error}</p>}{copyMessage && <p className="status status-info" role="status">{copyMessage}</p>}
    <div className="panel form-panel"><label className="field field-wide">选择上游<select value={draft.provider_id} onChange={(event) => chooseProvider(event.target.value)} disabled={busy}><option value="">选择上游</option>{providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}（{provider.id}）</option>)}</select><span className="field-hint">先选择上游，再从它的真实目录或官网候选选择模型；不会自动启用上游。</span></label></div>
    {selectedProvider && <ProviderTools client={client} provider={selectedProvider} selectedModel={draft.upstream_model} onSelectModel={chooseUpstream} disabled={busy} />}
    <div className="panel table-scroll"><table className="data-table"><caption>已配置模型</caption><thead><tr><th>完整公开 ID</th><th>所属上游</th><th>协议</th><th>能力</th><th>状态</th><th>调用方</th><th>操作</th></tr></thead><tbody>{models.length === 0 ? <tr><td colSpan={7} className="empty-state">暂无模型配置。</td></tr> : models.map((model) => { const provider = providers.find((item) => item.id === model.provider_id); const callerCount = callers.filter((caller) => caller.allowed_models.includes(model.id)).length; return <tr key={model.id}><td><strong>{model.id}</strong><small>{model.name}<button className="button button-small" type="button" onClick={() => void copyID(model.id)} disabled={busy}>复制</button></small></td><td>{provider?.name ?? model.provider_id}{provider && !provider.enabled ? <small className="status-text-warning">上游已停用</small> : null}</td><td>{model.protocols.map(protocolLabel).join("、")}</td><td>{model.input_images ? "支持图片" : "文本"}</td><td><span className={`status-dot ${model.enabled ? "status-dot-on" : "status-dot-off"}`}>{model.enabled ? "已启用" : "已停用"}</span></td><td>{callerCount ? `${callerCount} 个调用方` : "未授权调用方"}</td><td className="actions"><button className="button button-small" type="button" onClick={() => beginEdit(model)} disabled={busy}>编辑</button>{model.enabled && <button className="button button-small" type="button" onClick={() => void disable(model)} disabled={busy}>停用</button>}<button className="button button-small button-danger" type="button" onClick={() => void remove(model)} disabled={busy}>删除</button></td></tr>; })}</tbody></table></div>
    <form className="panel form-panel" onSubmit={save}>
      <div className="panel-heading"><div><h2>{editingID ? "编辑模型" : "新增模型"}</h2><p className="muted">默认值来自已选上游和官网候选；高级字段允许明确的自定义映射。</p></div></div>
      <div className="form-grid">
        <div className="field field-wide"><span>当前选择（保存后生效）</span>{draft.upstream_model ? <div className="model-access-list"><div className="actions"><code className="copy-value">上游 ID：{draft.upstream_model}</code><code className="copy-value">公开 ID：{draft.id || "将在选择模型后生成"}</code><button className="button button-small" type="button" onClick={() => void copyID(draft.id)} disabled={busy || !draft.id}>复制公开 ID</button></div></div> : <p className="field-hint">请先在上方模型目录选择一个模型；自定义上游 ID 在该目录的高级区填写。</p>}<span className="field-hint">新建时选择模型会生成名称、公开 ID、协议和图片能力；编辑既有模型时保留已有设置，需在高级区明确修改。</span></div>
        <details className="field field-wide advanced-section" open={Boolean(editingID)}><summary>高级模型字段：公开 ID、名称、协议、价格和能力</summary><div className="form-grid"><label className="field">公开 ID<input value={draft.id} readOnly={Boolean(editingID)} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, id: event.target.value }))} placeholder="provider/model-name" required /><span className="field-hint">ZCode 使用完整公开 ID，例如 bailian/qwen3.8-max；既有模型的公开 ID 保持只读。</span></label><label className="field">显示名称<input value={draft.name} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))} required /><span className="field-hint">仅影响管理页显示，不会改变上游 model ID。</span></label><fieldset className="field field-wide checkbox-group" disabled={busy}><legend>允许协议</legend>{allowedProtocols.map((protocol) => <label key={protocol} className="checkbox-field"><input type="checkbox" checked={draft.protocols.includes(protocol)} onChange={() => toggleProtocol(protocol)} disabled={busy} />{protocolLabel(protocol)}</label>)}<span className="field-hint">直连上游只允许其自身协议；适配器可选择三种。编辑不会自动改写已有协议集合。</span></fieldset><label className="field">输入价格<input type="number" min="0" step="any" value={draft.input_price} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, input_price: event.target.value }))} placeholder="未知" /><span className="field-hint">美元／每百万输入 token；留空表示未知，不会按 0 计价。</span></label><label className="field">输出价格<input type="number" min="0" step="any" value={draft.output_price} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, output_price: event.target.value }))} placeholder="未知" /><span className="field-hint">美元／每百万输出 token；留空表示未知，不会按 0 计价。</span></label><label className="field checkbox-field"><input type="checkbox" checked={draft.input_images} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, input_images: event.target.checked }))} />支持图片输入<span className="field-hint">勾选后才允许该公开模型接收图片输入；目录标注只是默认建议。</span></label><label className="field checkbox-field"><input type="checkbox" checked={draft.enabled} disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} />启用模型<span className="field-hint">停用会保留映射和权限记录；重新勾选并保存可恢复模型，不会启用上游或改调用方权限。</span></label></div></details>
      </div>
      <div className="actions form-actions"><button className="button button-primary" type="submit" disabled={busy}>{busy ? "保存中…" : editingID ? "保存模型" : draft.enabled ? "添加并启用模型" : "添加已停用模型"}</button>{editingID && <button className="button" type="button" disabled={busy} onClick={() => beginCreate(selectedProvider)}>取消编辑</button>}</div>
    </form>
  </section>;
}
