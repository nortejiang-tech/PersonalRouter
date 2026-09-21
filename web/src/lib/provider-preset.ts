import {
  getPreset,
  type ProviderAuthMode,
  type ProviderKind,
  type ProviderPreset,
  type ProviderProtocol,
// Node's strip-types test runner needs the explicit extension; Vite resolves
// the same source through the TypeScript module graph.
// @ts-expect-error TS5097: the test runner intentionally uses the .ts suffix.
} from "./presets.ts";

export interface ProviderPresetConnection {
  readonly protocol: ProviderProtocol;
  readonly endpoint: string;
  readonly source: string;
}

export type ProviderPresetConfigSource = "official" | "adapter" | "deployment";

/**
 * The catalog owns these fields. The optional input shape keeps this adapter
 * compatible with the original catalog while the catalog is being upgraded;
 * getPresetWithConnections always returns the complete contract.
 */
type CatalogInput = ProviderPreset & {
  readonly connections?: readonly ProviderPresetConnection[];
  readonly configSource?: ProviderPresetConfigSource;
  readonly verifiedAt?: string;
  readonly managedEndpoint?: boolean;
};

export interface ProviderPresetWithConnections extends ProviderPreset {
  readonly connections: readonly ProviderPresetConnection[];
  readonly configSource: ProviderPresetConfigSource;
  readonly verifiedAt: string;
  readonly managedEndpoint: boolean;
}

export interface ProviderPresetDraft {
  id: string;
  name: string;
  kind: ProviderKind;
  auth_mode: ProviderAuthMode;
  protocol: ProviderProtocol;
  endpoint: string;
  oauth_provider: string;
  enabled: boolean;
  secret: string;
  remove_secret: boolean;
}

export interface ApplyPresetOptions {
  editing: boolean;
  previousGeneratedId: string;
  previousGeneratedName: string;
}

export interface AppliedPresetDraft<T extends ProviderPresetDraft> {
  draft: T;
  generatedId: string;
  generatedName: string;
}

function fallbackConfigSource(preset: ProviderPreset): ProviderPresetConfigSource {
  if (preset.kind === "local") return "deployment";
  if (preset.authMode === "oauth") return "adapter";
  return "official";
}

function fallbackConnections(preset: ProviderPreset): readonly ProviderPresetConnection[] {
  return [{ protocol: preset.protocol, endpoint: preset.endpoint, source: preset.source }];
}

/** Return a deep copy of a catalog entry, including its connection list. */
export function getPresetWithConnections(id: string): ProviderPresetWithConnections | undefined {
  const source = getPreset(id) as CatalogInput | undefined;
  if (!source) return undefined;
  const connections = source.connections?.length ? source.connections : fallbackConnections(source);
  return {
    ...source,
    connections: connections.map((connection) => ({ ...connection })),
    configSource: source.configSource ?? fallbackConfigSource(source),
    verifiedAt: source.verifiedAt ?? "",
    managedEndpoint: source.managedEndpoint ?? source.authMode === "oauth",
  };
}

export function connectionForProtocol(
  preset: ProviderPresetWithConnections,
  protocol: ProviderProtocol,
): ProviderPresetConnection | undefined {
  return preset.connections.find((connection) => connection.protocol === protocol);
}

export function protocolChoices(preset: ProviderPresetWithConnections): readonly ProviderProtocol[] {
  return [...new Set(preset.connections.map((connection) => connection.protocol))];
}

export function sourceLabel(source: ProviderPresetConfigSource): string {
  if (source === "adapter") return "订阅入口自动配置";
  if (source === "deployment") return "EVO部署配置";
  return "官网配置已核实";
}

export function shouldDetachPreset(field: string): boolean {
  return field === "endpoint" || field === "kind" || field === "auth_mode" || field === "oauth_provider";
}

export function nextPresetSelection(selectedPreset: string, field: string): string {
  return shouldDetachPreset(field) ? "" : selectedPreset;
}

export function applyPresetSelection<T extends ProviderPresetDraft>(
  draft: T,
  preset: ProviderPresetWithConnections,
  options: ApplyPresetOptions,
): AppliedPresetDraft<T> {
  const connection = connectionForProtocol(preset, preset.protocol) ?? preset.connections[0];
  if (!connection) throw new Error("预设缺少协议连接");

  const canUpdateGeneratedId = !options.editing && (!draft.id || draft.id === options.previousGeneratedId);
  const canUpdateGeneratedName = !options.editing && (!draft.name || draft.name === options.previousGeneratedName);
  const nextId = canUpdateGeneratedId ? preset.id : draft.id;
  const nextName = canUpdateGeneratedName ? preset.label : draft.name;

  return {
    draft: {
      ...draft,
      id: nextId,
      name: nextName,
      kind: preset.kind,
      auth_mode: preset.authMode,
      protocol: connection.protocol,
      endpoint: preset.managedEndpoint ? "" : connection.endpoint,
      oauth_provider: preset.oauthProvider ?? "",
      enabled: false,
      secret: "",
      remove_secret: false,
    },
    generatedId: canUpdateGeneratedId ? nextId : "",
    generatedName: canUpdateGeneratedName ? nextName : "",
  };
}
