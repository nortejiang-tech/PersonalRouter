import test from 'node:test';
import assert from 'node:assert/strict';

import { PROVIDER_PRESETS, getPreset } from './presets.ts';
import type { ProviderPreset, ProviderPresetConnection } from './presets.ts';

const OFFICIAL_SOURCES: Record<string, string> = {
  'codex-subscription': 'https://developers.openai.com/codex/auth/',
  'glm-coding': 'https://docs.bigmodel.cn/cn/coding-plan/tool/others',
  'glm-api': 'https://docs.bigmodel.cn/cn/best-practice/case/ai-search-engine',
  'bailian-token-plan': 'https://help.aliyun.com/zh/model-studio/more-tools',
  'kimi-coding': 'https://www.kimi.com/code/docs/',
  'kimi-login':
    'https://www.kimi.com/code/docs/en/kimi-code-cli/configuration/providers',
  'deepseek-api': 'https://api-docs.deepseek.com/',
  'minimax-api': 'https://platform.minimax.cn/docs/api-reference/text-openai-api',
  'minimax-token-plan': 'https://platform.minimax.cn/docs/token-plan/other-tools',
  'evo-local': '',
};

function byId(id: string): ProviderPreset {
  const found = getPreset(id);
  assert.ok(found, `preset ${id} must exist`);
  return found;
}

function conn(p: ProviderPreset, protocol: string): ProviderPresetConnection {
  const found = p.connections.find((c) => c.protocol === protocol);
  assert.ok(found, `preset ${p.id} must declare a ${protocol} connection`);
  return found;
}

test('IDs are unique', () => {
  const ids = PROVIDER_PRESETS.map((p) => p.id);
  assert.equal(new Set(ids).size, ids.length);
});

test('preset count covers all ten catalog entries', () => {
  assert.equal(PROVIDER_PRESETS.length, 10);
  assert.deepEqual(
    [...PROVIDER_PRESETS.map((p) => p.id)].sort(),
    [
      'bailian-token-plan',
      'codex-subscription',
      'deepseek-api',
      'evo-local',
      'glm-api',
      'glm-coding',
      'kimi-coding',
      'kimi-login',
      'minimax-api',
      'minimax-token-plan',
    ],
  );
});

test('every preset verification is PENDING and has no auto-enabled field', () => {
  for (const preset of PROVIDER_PRESETS) {
    assert.equal(preset.verification, 'PENDING');
    assert.equal('enabled' in preset, false);
    assert.equal('apiKey' in preset, false);
    assert.equal('token' in preset, false);
    assert.equal('key' in preset, false);
  }
});

test('every preset verifiedAt is 2026-09-20', () => {
  for (const preset of PROVIDER_PRESETS) {
    assert.equal(preset.verifiedAt, '2026-09-20');
  }
});

test('default protocol and endpoint equal one of the connections', () => {
  for (const preset of PROVIDER_PRESETS) {
    const c = conn(preset, preset.protocol);
    assert.equal(c.endpoint, preset.endpoint);
    assert.equal(preset.source, OFFICIAL_SOURCES[preset.id]);
    assert.equal(c.source, preset.source);
  }
});

test('connection protocols are unique per preset', () => {
  for (const preset of PROVIDER_PRESETS) {
    const protocols = preset.connections.map((c) => c.protocol);
    assert.equal(new Set(protocols).size, protocols.length, preset.id);
    assert.ok(protocols.length >= 1, preset.id);
  }
});

test('only managed OAuth presets may have an empty endpoint', () => {
  for (const preset of PROVIDER_PRESETS) {
    if (preset.authMode === 'oauth') {
      assert.equal(preset.managedEndpoint, true, preset.id);
      assert.equal(preset.endpoint, '', preset.id);
      assert.equal(preset.protocol, 'adapter', preset.id);
      assert.equal(preset.configSource, 'adapter', preset.id);
    } else {
      assert.equal(preset.managedEndpoint, false, preset.id);
      assert.notEqual(preset.endpoint, '', preset.id);
      for (const c of preset.connections) {
        assert.notEqual(c.endpoint, '', `${preset.id}/${c.protocol}`);
      }
    }
  }
});

test('managedEndpoint is true only for the two OAuth presets', () => {
  const managed = PROVIDER_PRESETS.filter((p) => p.managedEndpoint).map((p) => p.id);
  assert.deepEqual(managed.sort(), ['codex-subscription', 'kimi-login']);
});

test('subscription and api kinds are not mixed up', () => {
  assert.equal(byId('glm-coding').kind, 'subscription');
  assert.equal(byId('glm-api').kind, 'api');
  assert.equal(byId('kimi-coding').kind, 'subscription');
  assert.equal(byId('kimi-login').kind, 'subscription');
  assert.equal(byId('codex-subscription').kind, 'subscription');
  assert.equal(byId('bailian-token-plan').kind, 'subscription');
  assert.equal(byId('minimax-token-plan').kind, 'subscription');
  assert.equal(byId('deepseek-api').kind, 'api');
  assert.equal(byId('minimax-api').kind, 'api');
  assert.equal(byId('evo-local').kind, 'local');
});

test('configSource is official except adapter OAuth and local deployment', () => {
  for (const preset of PROVIDER_PRESETS) {
    if (preset.id === 'codex-subscription' || preset.id === 'kimi-login') {
      assert.equal(preset.configSource, 'adapter');
    } else if (preset.id === 'evo-local') {
      assert.equal(preset.configSource, 'deployment');
      assert.equal(preset.source, '');
      assert.equal(conn(preset, 'openai').source, '');
    } else {
      assert.equal(preset.configSource, 'official', preset.id);
    }
  }
});

test('GLM Coding Plan maps all three protocols', () => {
  const p = byId('glm-coding');
  assert.equal(conn(p, 'openai').endpoint, 'https://open.bigmodel.cn/api/coding/paas/v4');
  assert.equal(conn(p, 'anthropic').endpoint, 'https://open.bigmodel.cn/api/anthropic/v1');
  assert.equal(conn(p, 'responses').endpoint, 'https://open.bigmodel.cn/api/v1');
  assert.equal(p.connections.length, 3);
});

test('GLM BigModel API only exposes the pay-as-you-go openai endpoint', () => {
  const p = byId('glm-api');
  assert.equal(p.connections.length, 1);
  assert.equal(conn(p, 'openai').endpoint, 'https://open.bigmodel.cn/api/paas/v4');
});

test('bailian token plan uses the exact endpoints per protocol', () => {
  const p = byId('bailian-token-plan');
  assert.equal(
    conn(p, 'openai').endpoint,
    'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
  );
  assert.equal(
    conn(p, 'anthropic').endpoint,
    'https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1',
  );
  assert.equal(conn(p, 'anthropic').source, 'https://help.aliyun.com/en/model-studio/opencode');
  assert.equal(p.connections.find((c) => c.protocol === 'responses'), undefined);
});

test('kimi coding plan keeps the anthropic default and openai alias', () => {
  const p = byId('kimi-coding');
  assert.equal(p.protocol, 'anthropic');
  assert.equal(conn(p, 'anthropic').endpoint, 'https://api.kimi.com/coding/v1');
  assert.equal(conn(p, 'openai').endpoint, 'https://api.kimi.com/coding/v1');
  assert.equal(
    conn(p, 'openai').source,
    'https://www.kimi.com/code/docs/en/third-party-tools/hermes.html',
  );
});

test('deepseek keeps full method URLs and per-protocol sources', () => {
  const p = byId('deepseek-api');
  assert.equal(conn(p, 'openai').endpoint, 'https://api.deepseek.com/chat/completions');
  assert.equal(conn(p, 'anthropic').endpoint, 'https://api.deepseek.com/anthropic/v1');
  assert.equal(conn(p, 'responses').endpoint, 'https://api.deepseek.com/responses');
  assert.equal(conn(p, 'anthropic').source, 'https://api-docs.deepseek.com/guides/anthropic_api/');
  assert.equal(conn(p, 'responses').source, 'https://api-docs.deepseek.com/guides/responses_api/');
});

test('minimax pay-as-you-go api exposes three protocols', () => {
  const p = byId('minimax-api');
  assert.equal(p.connections.length, 3);
  assert.equal(conn(p, 'openai').endpoint, 'https://api.minimax.cn/v1');
  assert.equal(conn(p, 'anthropic').endpoint, 'https://api.minimax.cn/anthropic/v1');
  assert.equal(conn(p, 'responses').endpoint, 'https://api.minimax.cn/v1');
});

test('minimax token plan never infers a responses connection', () => {
  const p = byId('minimax-token-plan');
  assert.equal(p.protocol, 'anthropic');
  assert.equal(p.connections.length, 2);
  assert.equal(p.connections.find((c) => c.protocol === 'responses'), undefined);
  assert.equal(conn(p, 'anthropic').endpoint, 'https://api.minimax.cn/anthropic/v1');
  assert.equal(conn(p, 'openai').endpoint, 'https://api.minimax.cn/v1');
  for (const c of p.connections) {
    assert.equal(c.source, 'https://platform.minimax.cn/docs/token-plan/other-tools');
  }
});

test('evo local deployment points at the loopback service', () => {
  const p = byId('evo-local');
  assert.equal(p.authMode, 'none');
  assert.equal(p.protocol, 'openai');
  assert.equal(conn(p, 'openai').endpoint, 'http://127.0.0.1:8001/v1');
  assert.equal(p.managedEndpoint, false);
});

test('oauth presets carry their oauthProvider', () => {
  assert.equal(byId('codex-subscription').oauthProvider, 'codex');
  assert.equal(byId('kimi-login').oauthProvider, 'kimi');
  for (const preset of PROVIDER_PRESETS) {
    if (preset.authMode !== 'oauth') {
      assert.equal('oauthProvider' in preset, false, preset.id);
    }
  }
});

test('notes never claim an account is already connected', () => {
  const banned = ['已连接', '已登录', '验证通过', '已验证账号'];
  for (const preset of PROVIDER_PRESETS) {
    for (const word of banned) {
      assert.equal(preset.note.includes(word), false, `${preset.id}: ${word}`);
    }
    assert.ok(preset.note.length > 0, preset.id);
  }
});

test('unknown id returns undefined', () => {
  assert.equal(getPreset('does-not-exist'), undefined);
});

test('getPreset returns a copy that does not pollute the catalog', () => {
  const copy = getPreset('glm-coding');
  assert.ok(copy);
  const mutableCopy = copy as unknown as { endpoint: string; label: string };
  mutableCopy.endpoint = 'https://evil.example.invalid';
  mutableCopy.label = 'mutated';
  assert.notEqual(getPreset('glm-coding')?.endpoint, 'https://evil.example.invalid');
  assert.notEqual(getPreset('glm-coding')?.label, 'mutated');
});

test('nested connection copies are independent from the catalog', () => {
  const copy = getPreset('deepseek-api');
  assert.ok(copy);
  assert.notEqual(copy.connections, byId('deepseek-api').connections);
  const mutableConnections = copy.connections as unknown as [
    { endpoint: string },
    { source: string },
    ...unknown[],
  ];
  mutableConnections[0].endpoint = 'https://evil.example.invalid';
  mutableConnections[1].source = 'https://evil.example.invalid';
  assert.equal(Object.isFrozen(copy.connections), true);
  const pushable = copy.connections as unknown as ProviderPresetConnection[];
  assert.throws(() => {
    pushable.push({
      protocol: 'responses',
      endpoint: 'https://evil.example.invalid',
      source: 'x',
    });
  }, TypeError);
  const fresh = byId('deepseek-api');
  assert.equal(fresh.connections.length, 3);
  assert.equal(conn(fresh, 'openai').endpoint, 'https://api.deepseek.com/chat/completions');
  assert.equal(
    conn(fresh, 'anthropic').source,
    'https://api-docs.deepseek.com/guides/anthropic_api/',
  );
  assert.notEqual(
    getPreset('deepseek-api')?.connections[0].endpoint,
    'https://evil.example.invalid',
  );
});

test('catalog connections are frozen against mutation', () => {
  const frozen = PROVIDER_PRESETS[0];
  assert.equal(Object.isFrozen(frozen), true);
  assert.equal(Object.isFrozen(frozen.connections), true);
  assert.equal(Object.isFrozen(frozen.connections[0]), true);
  const cast = frozen as { endpoint: string };
  assert.throws(() => {
    cast.endpoint = 'nope';
  }, TypeError);
});

test('readonly fields may be overridden only through an explicit cast', () => {
  const original = byId('glm-api');
  const mutated = { ...original, endpoint: 'https://changed.invalid' } as ProviderPreset;
  assert.equal(mutated.endpoint, 'https://changed.invalid');
  assert.equal(byId('glm-api').endpoint, 'https://open.bigmodel.cn/api/paas/v4');
  assert.equal(original.verification, 'PENDING');
});
