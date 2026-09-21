import assert from "node:assert/strict";
import test from "node:test";
import type { ProviderPresetDraft, ProviderPresetWithConnections } from "./provider-preset.ts";
import {
  applyPresetSelection,
  connectionForProtocol,
  nextPresetSelection,
  protocolChoices,
} from "./provider-preset.ts";

const preset: ProviderPresetWithConnections = {
  id: "example",
  label: "Example",
  kind: "api",
  authMode: "api_key",
  protocol: "openai",
  endpoint: "https://example.invalid/openai",
  note: "test",
  source: "https://example.invalid/docs",
  verification: "PENDING",
  connections: [
    { protocol: "openai", endpoint: "https://example.invalid/openai", source: "https://example.invalid/openai" },
    { protocol: "anthropic", endpoint: "https://example.invalid/anthropic", source: "https://example.invalid/anthropic" },
  ],
  configSource: "official",
  verifiedAt: "2026-09-20",
  managedEndpoint: false,
};

const draft = (overrides: Partial<ProviderPresetDraft> = {}): ProviderPresetDraft => ({
  id: "",
  name: "",
  kind: "local",
  auth_mode: "none",
  protocol: "responses",
  endpoint: "",
  oauth_provider: "",
  enabled: true,
  secret: "unsaved-secret",
  remove_secret: true,
  ...overrides,
});

test("protocol choices map only supported connections and update endpoints", () => {
  assert.deepEqual(protocolChoices(preset), ["openai", "anthropic"]);
  assert.equal(connectionForProtocol(preset, "anthropic")?.endpoint, "https://example.invalid/anthropic");
  assert.equal(connectionForProtocol(preset, "responses"), undefined);
});

test("first selection generates id and name, clears secret, and disables draft", () => {
  const result = applyPresetSelection(draft(), preset, { editing: false, previousGeneratedId: "", previousGeneratedName: "" });
  assert.equal(result.draft.id, "example");
  assert.equal(result.draft.name, "Example");
  assert.equal(result.draft.enabled, false);
  assert.equal(result.draft.secret, "");
  assert.equal(result.draft.remove_secret, false);
  assert.equal(result.generatedId, "example");
  assert.equal(result.generatedName, "Example");
});

test("switching presets updates generated values but preserves manual id and name", () => {
  const generated = applyPresetSelection(draft(), preset, { editing: false, previousGeneratedId: "", previousGeneratedName: "" });
  const switched = applyPresetSelection(generated.draft, { ...preset, id: "other", label: "Other" }, {
    editing: false,
    previousGeneratedId: generated.generatedId,
    previousGeneratedName: generated.generatedName,
  });
  assert.equal(switched.draft.id, "other");
  assert.equal(switched.draft.name, "Other");

  const manual = applyPresetSelection(draft({ id: "my-id", name: "My provider" }), preset, {
    editing: false,
    previousGeneratedId: "",
    previousGeneratedName: "",
  });
  const manualSwitch = applyPresetSelection(manual.draft, { ...preset, id: "other", label: "Other" }, {
    editing: false,
    previousGeneratedId: manual.generatedId,
    previousGeneratedName: manual.generatedName,
  });
  assert.equal(manualSwitch.draft.id, "my-id");
  assert.equal(manualSwitch.draft.name, "My provider");
});

test("editing an existing provider never changes its id", () => {
  const result = applyPresetSelection(draft({ id: "existing", name: "Existing" }), preset, {
    editing: true,
    previousGeneratedId: "",
    previousGeneratedName: "",
  });
  assert.equal(result.draft.id, "existing");
  assert.equal(result.draft.name, "Existing");
  assert.equal(result.generatedId, "");
});

test("only endpoint, kind, and auth edits detach the selected preset", () => {
  assert.equal(nextPresetSelection("example", "name"), "example");
  assert.equal(nextPresetSelection("example", "id"), "example");
  assert.equal(nextPresetSelection("example", "secret"), "example");
  assert.equal(nextPresetSelection("example", "enabled"), "example");
  assert.equal(nextPresetSelection("example", "protocol"), "example");
  assert.equal(nextPresetSelection("example", "endpoint"), "");
  assert.equal(nextPresetSelection("example", "kind"), "");
  assert.equal(nextPresetSelection("example", "auth_mode"), "");
  assert.equal(nextPresetSelection("example", "oauth_provider"), "");
});

test("managed preset connections never expose an adapter endpoint", () => {
  const managed = { ...preset, managedEndpoint: true };
  const result = applyPresetSelection(draft(), managed, { editing: false, previousGeneratedId: "", previousGeneratedName: "" });
  assert.equal(result.draft.endpoint, "");
});
