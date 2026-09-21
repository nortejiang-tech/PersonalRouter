import { useEffect, useMemo, useRef, useState } from "react";
import type { AdminClient, Provider, ProviderModelRef, ProviderTest, Protocol } from "../api";
import { copyText } from "../lib/clipboard";
import { officialModelsForProvider, type OfficialModelCatalog } from "../lib/model-catalog";

interface ProviderToolsProps {
  client: AdminClient;
  provider: Provider;
  onSelectModel?: (model: { id: string; input_images: boolean } | null) => void;
  selectedModel?: string;
  disabled?: boolean;
}

type DiscoveryState = {
  status: "loading" | "ok" | "unsupported" | "error";
  message: string;
  models: ProviderModelRef[];
  checkedAt: string;
  upstreamStatus: number;
  providerEnabled: boolean;
  catalog?: OfficialModelCatalog;
};

const emptyDiscovery = (): DiscoveryState => ({
  status: "loading",
  message: "正在读取上游模型列表…",
  models: [],
  checkedAt: "",
  upstreamStatus: 0,
  providerEnabled: true,
});

function protocolLabel(protocol: string): string {
  if (protocol === "openai") return "OpenAI 兼容";
  if (protocol === "responses") return "Responses";
  if (protocol === "anthropic") return "Anthropic";
  return protocol;
}

function formatCheckedAt(value: string): string {
  if (!value) return "尚未检查";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function directProtocols(provider: Provider): Array<Exclude<Protocol, "adapter">> {
  if (provider.protocol === "adapter") return ["openai", "responses", "anthropic"];
  return [provider.protocol];
}

function discoveryStatusMessage(state: DiscoveryState): string {
  if (state.status === "loading") return state.message;
  if (state.status === "ok") return "上游返回的模型列表";
  if (state.status === "unsupported") return state.catalog ? "上游不提供模型发现；以下是官网候选，账号可用性待测试。" : "上游不提供模型发现；请在高级区填写自定义模型 ID。";
  return state.catalog ? "模型发现失败；以下是官网候选，账号可用性待测试。" : "模型发现失败；请在高级区填写自定义模型 ID。";
}

export function ProviderTools({ client, provider, onSelectModel, selectedModel, disabled = false }: ProviderToolsProps) {
  const [discovery, setDiscovery] = useState<DiscoveryState>(emptyDiscovery);
  const [testModel, setTestModel] = useState(selectedModel ?? "");
  const [testProtocol, setTestProtocol] = useState<Exclude<Protocol, "adapter">>(directProtocols(provider)[0]);
  const [customModel, setCustomModel] = useState("");
  const [test, setTest] = useState<ProviderTest | null>(null);
  const [testBusy, setTestBusy] = useState(false);
  const [testError, setTestError] = useState("");
  const [copyMessage, setCopyMessage] = useState("");
  const generation = useRef(0);
  const testGeneration = useRef(0);

  const fallbackCatalog = useMemo(() => officialModelsForProvider(provider), [provider]);
  const displayedModels = discovery.status === "ok" ? discovery.models : discovery.status === "loading" ? [] : (fallbackCatalog?.models ?? []);
  const isAdapter = provider.protocol === "adapter";
  const interactionBusy = disabled || testBusy;

  useEffect(() => {
    const currentGeneration = ++generation.current;
    testGeneration.current += 1;
    setDiscovery({ ...emptyDiscovery(), catalog: fallbackCatalog, providerEnabled: provider.enabled });
    setTest(null);
    setTestError("");
    setTestBusy(false);
    setTestModel(selectedModel ?? "");
    setCustomModel("");
    setTestProtocol(directProtocols(provider)[0]);
    void client.discoverProviderModels(provider.id).then((result) => {
      if (generation.current !== currentGeneration) return;
      setDiscovery({
        status: result.status === "ok" ? "ok" : result.status === "unsupported" ? "unsupported" : "error",
        message: result.message,
        models: result.models ?? [],
        checkedAt: result.checked_at,
        upstreamStatus: result.upstream_status,
        providerEnabled: result.provider_enabled,
        catalog: fallbackCatalog,
      });
    }).catch((error: unknown) => {
      if (generation.current !== currentGeneration) return;
      setDiscovery({
        ...emptyDiscovery(),
        status: "error",
        message: error instanceof Error ? error.message : "模型发现失败",
        catalog: fallbackCatalog,
        providerEnabled: provider.enabled,
      });
    });
    return () => { generation.current += 1; testGeneration.current += 1; };
  }, [client, fallbackCatalog, provider]);

  useEffect(() => {
    if (selectedModel !== undefined) {
      setTestModel(selectedModel);
      setTest(null);
      setTestError("");
      setTestBusy(false);
      testGeneration.current += 1;
    }
  }, [selectedModel]);

  const chooseModel = (id: string, inputImages: boolean) => {
    setTestModel(id);
    setCustomModel("");
    setTest(null);
    setTestError("");
    setTestBusy(false);
    testGeneration.current += 1;
    onSelectModel?.({ id, input_images: inputImages });
  };

  const clearModel = () => {
    setTestModel("");
    setCustomModel("");
    setTest(null);
    setTestError("");
    setTestBusy(false);
    testGeneration.current += 1;
    onSelectModel?.(null);
  };

  const chooseCustomModel = () => {
    const id = customModel.trim();
    if (!id) {
      setTestError("请输入上游模型 ID。");
      return;
    }
    chooseModel(id, false);
  };

  const runTest = async () => {
    const model = testModel.trim();
    if (!model) {
      setTestError("先选择或填写上游模型 ID。");
      return;
    }
    setTestBusy(true);
    setTestError("");
    setTest(null);
    const currentTestGeneration = ++testGeneration.current;
    try {
      const result = await client.testProvider(provider.id, { model, protocol: testProtocol });
      if (testGeneration.current !== currentTestGeneration) return;
      setTest(result);
    } catch (error: unknown) {
      if (testGeneration.current !== currentTestGeneration) return;
      setTestError(error instanceof Error ? error.message : "连通测试失败");
    } finally {
      if (testGeneration.current === currentTestGeneration) setTestBusy(false);
    }
  };

  const copyModel = async (id: string) => {
    setCopyMessage((await copyText(id)) ? "上游模型 ID 已复制" : "复制失败，请手动选择模型 ID");
    window.setTimeout(() => setCopyMessage(""), 2200);
  };

  return (
    <section className="panel provider-tools" aria-labelledby={`provider-tools-${provider.id}`}>
      <div className="panel-heading">
        <div>
          <h3 id={`provider-tools-${provider.id}`}>模型发现与连接测试</h3>
          <p className="muted">读取已保存上游的模型目录，不会自动发送推理测试。</p>
        </div>
        <button className="button button-small" type="button" disabled={interactionBusy} onClick={() => {
          const currentGeneration = ++generation.current;
          testGeneration.current += 1;
          setDiscovery({ ...emptyDiscovery(), catalog: fallbackCatalog, providerEnabled: provider.enabled });
          setTestBusy(false);
          setTest(null);
          void client.discoverProviderModels(provider.id).then((result) => {
            if (generation.current !== currentGeneration) return;
            setDiscovery({
            status: result.status === "ok" ? "ok" : result.status === "unsupported" ? "unsupported" : "error",
            message: result.message,
            models: result.models ?? [],
            checkedAt: result.checked_at,
            upstreamStatus: result.upstream_status,
            providerEnabled: result.provider_enabled,
            catalog: fallbackCatalog,
            });
          }).catch((error: unknown) => {
            if (generation.current !== currentGeneration) return;
            setDiscovery({ ...emptyDiscovery(), status: "error", message: error instanceof Error ? error.message : "模型发现失败", catalog: fallbackCatalog, providerEnabled: provider.enabled });
          });
        }}>刷新模型列表</button>
      </div>
      <p className="field-hint">{discoveryStatusMessage(discovery)} · 检查时间：{formatCheckedAt(discovery.checkedAt || discovery.catalog?.checkedAt || "")}{discovery.upstreamStatus ? ` · 上游 HTTP ${discovery.upstreamStatus}` : ""}</p>
      {!discovery.providerEnabled && discovery.status !== "loading" && <p className="status status-info">上游当前已停用；仍可执行测试，客户端调用前需启用通道。</p>}
      {discovery.status !== "ok" && discovery.message && <p className="status status-error" role="status">真实发现结果：{discovery.message}</p>}
      {displayedModels.length > 0 || testModel ? <label className="field">选择上游模型<select value={testModel} disabled={interactionBusy} onChange={(event) => { if (!event.target.value) { clearModel(); return; } const model = displayedModels.find((item) => item.id === event.target.value); if (model) chooseModel(model.id, model.input_images === true); }}><option value="">选择模型</option>{testModel && !displayedModels.some((model) => model.id === testModel) && <option value={testModel}>{testModel} · 当前自定义模型</option>}{displayedModels.map((model) => <option key={model.id} value={model.id}>{model.id}{model.input_images === true ? " · 支持图片" : ""}</option>)}</select><span className="field-hint">选择后可复制完整上游 ID；真实返回目录优先。当前不在目录中的已选模型会保留为自定义选项。</span></label> : <p className="muted">暂无可显示的模型目录。可在高级区填写自定义上游模型 ID。</p>}
      {testModel && <p className="model-id-line"><code>{testModel}</code><button className="button button-small" type="button" onClick={() => void copyModel(testModel)}>复制上游 ID</button></p>}
      {discovery.catalog && (discovery.status === "unsupported" || discovery.status === "error") && <p className="field-hint">官网候选来源：<a href={discovery.catalog.source} target="_blank" rel="noreferrer">{discovery.catalog.label}</a>（核实于 {discovery.catalog.checkedAt}）。候选不代表当前账号已授权。</p>}
      {copyMessage && <p className="status status-info" role="status">{copyMessage}</p>}
      <details className="advanced-section"><summary>高级：自定义上游模型 ID</summary><div className="form-grid"><label className="field field-wide">上游模型 ID<input value={customModel} disabled={interactionBusy} onChange={(event) => setCustomModel(event.target.value)} placeholder="例如 qwen3.8-max" /><span className="field-hint">必须填写供应商实际接受的原始模型 ID；不要填写公开 ID 前缀。</span></label><button className="button button-small" type="button" disabled={interactionBusy} onClick={chooseCustomModel}>使用此 ID</button></div></details>
      <div className="form-grid provider-test-form">
        <label className="field">测试模型<input value={testModel} readOnly aria-readonly="true" /><span className="field-hint">从上方目录或高级区选择，测试使用该真实上游 ID。</span></label>
        <label className="field">测试协议<select value={testProtocol} onChange={(event) => { setTestProtocol(event.target.value as Exclude<Protocol, "adapter">); setTest(null); setTestBusy(false); testGeneration.current += 1; }} disabled={interactionBusy || !isAdapter}>{directProtocols(provider).map((protocol) => <option key={protocol} value={protocol}>{protocolLabel(protocol)}</option>)}</select><span className="field-hint">直连仅使用上游自身协议；适配器可选三种。</span></label>
        <div className="actions"><button className="button button-primary" type="button" disabled={interactionBusy || discovery.status === "loading"} onClick={() => void runTest()}>{testBusy ? "测试中…" : "连通测试"}</button><span className="field-hint">发送固定短请求，会消耗少量供应商额度，不计入调用方统计；不会改变启用状态或权限。</span></div>
      </div>
      {testError && <p className="status status-error" role="alert">{testError}</p>}
      {test && <p className={`status ${test.status === "ok" ? "status-success" : "status-error"}`} role="status">{test.status === "ok" ? "连通测试成功" : "连通测试失败"} · {test.message} · 模型 {test.model} · 协议 {protocolLabel(test.protocol)} · 上游 HTTP {test.upstream_status || "未知"} · 延迟 {test.latency_ms} ms · 检查时间 {formatCheckedAt(test.checked_at)}</p>}
    </section>
  );
}
