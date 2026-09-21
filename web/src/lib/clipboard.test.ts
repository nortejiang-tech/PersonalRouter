import assert from "node:assert/strict";
import test from "node:test";
import { copyText } from "./clipboard.ts";

function installDocument(execResult: boolean | (() => boolean)) {
  const originalDocument = globalThis.document;
  const originalNavigator = globalThis.navigator;
  let removed = false;
  let focused = false;
  let appended = false;
  const active = { focus: () => { focused = true; } } as unknown as HTMLElement;
  const textarea = {
    value: "",
    style: {} as CSSStyleDeclaration,
    setAttribute: () => undefined,
    focus: () => undefined,
    select: () => undefined,
    remove: () => { removed = true; appended = false; },
  } as unknown as HTMLTextAreaElement;
  const fakeDocument = {
    activeElement: active,
    body: { appendChild: () => { appended = true; } },
    createElement: () => textarea,
    execCommand: () => typeof execResult === "function" ? execResult() : execResult,
    getSelection: () => null,
  } as unknown as Document;
  Object.defineProperty(globalThis, "document", { configurable: true, value: fakeDocument });
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: {} });
  return {
    state: () => ({ removed, focused, appended }),
    restore: () => {
      Object.defineProperty(globalThis, "document", { configurable: true, value: originalDocument });
      Object.defineProperty(globalThis, "navigator", { configurable: true, value: originalNavigator });
    },
  };
}

test("copyText falls back to textarea execCommand and cleans up", async () => {
  const env = installDocument(true);
  try {
    assert.equal(await copyText("safe command"), true);
    assert.deepEqual(env.state(), { removed: true, focused: true, appended: false });
  } finally {
    env.restore();
  }
});

test("copyText reports fallback failure and still cleans up", async () => {
  const env = installDocument(false);
  try {
    assert.equal(await copyText("safe command"), false);
    assert.deepEqual(env.state(), { removed: true, focused: true, appended: false });
  } finally {
    env.restore();
  }
});
