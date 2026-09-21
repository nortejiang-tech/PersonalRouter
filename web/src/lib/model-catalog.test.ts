import test from "node:test";
import assert from "node:assert/strict";

import {
  officialModelsForProvider,
  suggestModelID,
  type OfficialModelCatalog,
} from "./model-catalog.ts";

const CHECKED_AT = "2026-09-20";

test("bailian token plan personal: 11 models, exact order, casing, image defaults", () => {
  const catalog = officialModelsForProvider({
    endpoint: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
    kind: "subscription",
    auth_mode: "api_key",
  });
  assert.ok(catalog, "expected a catalog");
  assert.equal(catalog!.checkedAt, CHECKED_AT);
  assert.equal(
    catalog!.source,
    "https://help.aliyun.com/zh/model-studio/token-plan-personal-overview",
  );
  assert.match(catalog!.label, /个人版/);
  assert.match(catalog!.label, /官网候选/);
  assert.equal(catalog!.models.length, 11);
  assert.deepEqual(
    catalog!.models.map((m) => [m.id, m.input_images]),
    [
      ["qwen3.8-max", true],
      ["qwen3.8-flash", true],
      ["qwen3.7-max", false],
      ["qwen3.7-plus", true],
      ["qwen3.6-flash", true],
      ["deepseek-v4.1-flash", true],
      ["deepseek-v4-pro", false],
      ["deepseek-v4-pro-0813", false],
      ["deepseek-v4-flash-0731", false],
      ["glm-5.3", false],
      ["glm-5.2", false],
    ],
  );
});

test("bailian anthropic path also matches same catalog", () => {
  const a = officialModelsForProvider({
    endpoint: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1",
    kind: "subscription",
    auth_mode: "api_key",
  });
  assert.ok(a);
  assert.equal(a!.models.length, 11);
});

test("trailing single slash is accepted, double trailing slash rejected", () => {
  const ok = officialModelsForProvider({
    endpoint: "https://api.deepseek.com/chat/completions/",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(ok);
  assert.equal(ok!.models.length, 2);

  const bad = officialModelsForProvider({
    endpoint: "https://api.deepseek.com/chat/completions//",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.equal(bad, undefined);
});

test("subscription and api kinds are isolated", () => {
  // DeepSeek is api-only: subscription must not get its catalog.
  assert.equal(
    officialModelsForProvider({
      endpoint: "https://api.deepseek.com/chat/completions",
      kind: "subscription",
      auth_mode: "api_key",
    }),
    undefined,
  );
  // Bailian is subscription-only: api must not get its catalog.
  assert.equal(
    officialModelsForProvider({
      endpoint: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
      kind: "api",
      auth_mode: "api_key",
    }),
    undefined,
  );
});

test("minimax subscription gets only M3, api gets full list", () => {
  const sub = officialModelsForProvider({
    endpoint: "https://api.minimax.cn/v1",
    kind: "subscription",
    auth_mode: "api_key",
  });
  assert.ok(sub);
  assert.deepEqual(
    sub!.models.map((m) => [m.id, m.input_images]),
    [["MiniMax-M3", true]],
  );
  assert.equal(
    sub!.source,
    "https://platform.minimax.cn/docs/token-plan/other-tools",
  );

  const api = officialModelsForProvider({
    endpoint: "https://api.minimax.cn/anthropic/v1",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(api);
  assert.equal(api!.models.length, 8);
  assert.equal(api!.models[0].id, "MiniMax-M3");
  assert.equal(api!.models[0].input_images, true);
  assert.equal(api!.models[1].id, "MiniMax-M2.7");
  assert.equal(api!.models[7].id, "MiniMax-M2");
});

test("glm coding plan endpoints, plain GLM api not borrowed", () => {
  for (
    const p of [
      "https://open.bigmodel.cn/api/coding/paas/v4",
      "https://open.bigmodel.cn/api/anthropic/v1",
      "https://open.bigmodel.cn/api/v1",
    ]
  ) {
    const c = officialModelsForProvider({
      endpoint: p,
      kind: "subscription",
      auth_mode: "api_key",
    });
    assert.ok(c, p);
    assert.deepEqual(c!.models.map((m) => [m.id, m.input_images]), [
      ["glm-5.2", false],
    ]);
  }
  // Plain pay-as-you-go GLM api kind now has its OWN independently sourced
  // candidate (docs.bigmodel.cn), distinct from the Coding Plan subscription.
  const plain = officialModelsForProvider({
    endpoint: "https://open.bigmodel.cn/api/paas/v4",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(plain, "plain GLM api should have a candidate");
  assert.equal(plain!.label, "GLM BigModel API 官网候选");
  assert.equal(
    plain!.source,
    "https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2",
  );
  assert.equal(plain!.checkedAt, CHECKED_AT);
  assert.deepEqual(plain!.models.map((m) => [m.id, m.input_images]), [
    ["glm-5.2", false],
  ]);

  // Kind isolation is still enforced in both directions on this endpoint:
  // the subscription catalog must not be reachable as api, and the api
  // catalog must not be reachable as subscription on the api-only path.
  // The Coding Plan subscription does NOT cover the plain /api/paas/v4 path,
  // so the two sources can never be confused for one endpoint.
  assert.equal(
    officialModelsForProvider({
      endpoint: "https://open.bigmodel.cn/api/paas/v4",
      kind: "subscription",
      auth_mode: "api_key",
    }),
    undefined,
  );
  assert.notEqual(
    plain!.source,
    "https://docs.bigmodel.cn/cn/coding-plan/tool/others",
  );
  assert.equal(
    officialModelsForProvider({
      endpoint: "https://open.bigmodel.cn/api/paas/v4/extra",
      kind: "api",
      auth_mode: "api_key",
    }),
    undefined,
  );
  assert.equal(
    officialModelsForProvider({
      endpoint: "https://open.bigmodel.cn/api/paas/v4",
      kind: "oauth",
      auth_mode: "oauth",
    }),
    undefined,
  );
});

test("kimi coding plan models default input_images false", () => {
  const c = officialModelsForProvider({
    endpoint: "https://api.kimi.com/coding/v1",
    kind: "subscription",
    auth_mode: "api_key",
  });
  assert.ok(c);
  assert.deepEqual(
    c!.models.map((m) => [m.id, m.input_images]),
    [
      ["k3", false],
      ["k3-256k", false],
      ["kimi-for-coding", false],
      ["kimi-for-coding-highspeed", false],
    ],
  );
});

test("malicious lookalike domains rejected", () => {
  const bad = [
    "https://token-plan.cn-beijing.maas.aliyuncs.com.evil.com/compatible-mode/v1",
    "https://evil-token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
    "https://token-plan.cn-beijing.maas.aliyuncs.net/compatible-mode/v1",
    "https://open.bigmodel.cn.evil.com/api/v1",
    "https://openbigmodel.cn/api/v1",
    "https://api.kimi.com.evil.io/coding/v1",
    "https://api-deepseek.com/chat/completions",
    "https://api.minimax.cn.evil.com/v1",
    "https://api.minimax.com/v1",
  ];
  for (const endpoint of bad) {
    for (const kind of ["api", "subscription"]) {
      assert.equal(
        officialModelsForProvider({ endpoint, kind, auth_mode: "api_key" }),
        undefined,
        endpoint,
      );
    }
  }
});

test("userinfo, query, fragment, http, non-443 port rejected", () => {
  const bad = [
    "https://user:pass@api.deepseek.com/chat/completions",
    "https://api.deepseek.com:443@evil.com/chat/completions",
    "https://api.deepseek.com/chat/completions?key=abc",
    "https://api.deepseek.com/chat/completions#frag",
    "http://api.deepseek.com/chat/completions",
    "https://api.deepseek.com:8443/chat/completions",
    "https://api.deepseek.com:80/chat/completions",
    "ftp://api.deepseek.com/chat/completions",
    "//api.deepseek.com/chat/completions",
    "api.deepseek.com/chat/completions",
  ];
  for (const endpoint of bad) {
    assert.equal(
      officialModelsForProvider({
        endpoint,
        kind: "api",
        auth_mode: "api_key",
      }),
      undefined,
      endpoint,
    );
  }
  // explicit 443 is fine
  const ok = officialModelsForProvider({
    endpoint: "https://api.deepseek.com:443/chat/completions",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(ok);
});

test("unknown endpoints and unknown paths are undefined", () => {
  const bad = [
    "https://api.deepseek.com/v1/chat/completions",
    "https://api.deepseek.com/completions",
    "https://api.kimi.com/v1/coding",
    "https://open.bigmodel.cn/api/coding/paas/v3",
    "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v2",
    "https://totally-unknown.example.com/v1",
  ];
  for (const endpoint of bad) {
    for (const kind of ["api", "subscription"]) {
      assert.equal(
        officialModelsForProvider({ endpoint, kind, auth_mode: "api_key" }),
        undefined,
        endpoint,
      );
    }
  }
});

test("oauth and local auth modes never produce candidates", () => {
  for (const auth_mode of ["oauth", "oauth2", "local", "", "api-key", "API_KEY"]) {
    assert.equal(
      officialModelsForProvider({
        endpoint: "https://api.deepseek.com/chat/completions",
        kind: "api",
        auth_mode,
      }),
      undefined,
      auth_mode,
    );
    assert.equal(
      officialModelsForProvider({
        endpoint: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
        kind: "subscription",
        auth_mode,
      }),
      undefined,
      auth_mode,
    );
  }
});

test("results are deep-frozen and cannot pollute later results", () => {
  const first = officialModelsForProvider({
    endpoint: "https://api.deepseek.com",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(first);
  assert.ok(Object.isFrozen(first));
  assert.ok(Object.isFrozen(first!.models));
  assert.ok(Object.isFrozen(first!.models[0]));

  // Attempt mutation via explicit mutable casts; must throw, not corrupt.
  const mutableCatalog = first as unknown as {
    label: string;
    models: { id: string; input_images: boolean }[];
  };
  assert.throws(() => {
    mutableCatalog.label = "hijacked";
  }, TypeError);
  assert.throws(() => {
    mutableCatalog.models[0]!.id = "evil-model";
  }, TypeError);
  assert.throws(() => {
    mutableCatalog.models.push({ id: "injected", input_images: true });
  }, TypeError);

  const second = officialModelsForProvider({
    endpoint: "https://api.deepseek.com",
    kind: "api",
    auth_mode: "api_key",
  });
  assert.ok(second);
  assert.equal(second!.label, "DeepSeek API 官网候选");
  assert.deepEqual(second!.models.map((m) => m.id), [
    "deepseek-flash",
    "deepseek-v4-pro",
  ]);
  assert.equal(second!.models[0].id, "deepseek-flash");
});

test("malformed provider input returns undefined", () => {
  const weird: unknown[] = [
    null,
    undefined,
    {},
    { endpoint: 1, kind: "api", auth_mode: "api_key" },
    { endpoint: "https://api.deepseek.com", kind: 2, auth_mode: "api_key" },
    { endpoint: "https://api.deepseek.com", kind: "api", auth_mode: 3 },
    { endpoint: "", kind: "api", auth_mode: "api_key" },
  ];
  for (const p of weird) {
    assert.equal(
      officialModelsForProvider(p as never),
      undefined,
      JSON.stringify(p),
    );
  }
});

test("catalog never claims authorization or test success", () => {
  const catalogs: OfficialModelCatalog[] = [];
  for (const endpoint of [
    "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
    "https://open.bigmodel.cn/api/v1",
    "https://api.kimi.com/coding/v1",
    "https://api.deepseek.com",
    "https://api.minimax.cn/v1",
  ]) {
    for (const kind of ["api", "subscription"]) {
      const c = officialModelsForProvider({ endpoint, kind, auth_mode: "api_key" });
      if (c) catalogs.push(c);
    }
  }
  assert.ok(catalogs.length >= 5);
  for (const c of catalogs) {
    assert.doesNotMatch(c.label, /已授权|authorized|测试成功|tested ok/i);
    assert.ok(c.source.startsWith("https://"));
    assert.equal(c.checkedAt, CHECKED_AT);
  }
});

test("suggestModelID happy paths preserve casing and slashes", () => {
  assert.equal(suggestModelID("bailian", "qwen3.8-max"), "bailian/qwen3.8-max");
  assert.equal(suggestModelID("My_Prov", "MiniMax-M3"), "My_Prov/MiniMax-M3");
  assert.equal(suggestModelID("p-1", "vendor/model:v2.1"), "p-1/vendor/model:v2.1");
});

test("suggestModelID rejects unsafe or malformed input", () => {
  const bad: [unknown, unknown][] = [
    ["", "model"],
    ["prov", ""],
    ["/prov", "model"],
    ["prov", "/model"],
    ["prov/", "model"],
    ["prov", "model/"],
    ["pro..v", "model"],
    ["prov", "mod..el"],
    ["pro v", "model"],
    ["prov", "mod el"],
    ["prov", "mod?el"],
    ["prov", "mod#el"],
    ["prov", "mod\\el"],
    ["prov", "modél"],
    ["prov", "mod\nel"],
    [null, "model"],
    ["prov", undefined],
    [123, "model"],
  ];
  for (const [p, u] of bad) {
    assert.equal(suggestModelID(p as never, u as never), "", `${p} / ${u}`);
  }
});

test("suggestModelID rejects segments not starting with an ASCII letter or digit", () => {
  const bad: [unknown, unknown][] = [
    ["provider", "._bad"],
    ["-provider", "model"],
    ["provider", "_bad"],
    ["provider", ".bad"],
    ["provider", ":bad"],
    ["provider", "-bad"],
    ["_provider", "model"],
    ["provider", "nested/_bad"],
    ["provider", "nested/.bad"],
    ["provider", "nested/-deep"],
    ["provider", "nested/:deep"],
  ];
  for (const [p, u] of bad) {
    assert.equal(suggestModelID(p as never, u as never), "", `${p} / ${u}`);
  }

  // Leading ASCII alnum is enough; `._:-` remain legal inside a segment.
  assert.equal(suggestModelID("p1", "a._:-ok"), "p1/a._:-ok");
  assert.equal(suggestModelID("9x", "a_b.c-d:e"), "9x/a_b.c-d:e");
  assert.equal(suggestModelID("p1", "sub/a._:-ok"), "p1/sub/a._:-ok");
});

test("suggestModelID length limit 128", () => {
  const provider = "a".repeat(63);
  const upstream = "b".repeat(64);
  const ok = suggestModelID(provider, upstream);
  assert.equal(ok.length, 128);
  assert.equal(ok, `${provider}/${upstream}`);

  const tooLong = suggestModelID("a".repeat(64), "b".repeat(65));
  assert.equal(tooLong, "");
});
