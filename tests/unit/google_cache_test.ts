import { assert, assertEquals, assertRejects } from "@std/assert";
import { explicitCache } from "../../src/ai/google_cache.ts";
import { createAiGenerator } from "../../src/ai/providers.ts";
import { SDK_PAYLOAD_FORMAT } from "../../src/ai/messages.ts";
import type { AiMessage, AiRequest } from "../../src/ai/types.ts";
import { parseConfig } from "../../src/config.ts";

const message = (messageId: number, overrides: Partial<AiMessage> = {}): AiMessage => ({
  chatId: -1,
  messageId,
  sentAt: 1_700_000_000,
  userId: 7,
  username: "user",
  role: "user",
  messageType: "text",
  text: "context",
  media: [],
  ...overrides,
});
const request = (overrides: Partial<AiRequest> = {}): AiRequest => ({
  sessionId: 9,
  provider: "gemini",
  model: "gemini-3-flash-preview",
  systemPrompt: "system",
  messages: [message(1), message(2)],
  newMessages: [message(2)],
  historyRebuildLossy: false,
  allowCodeExecution: true,
  explicitCache: true,
  state: {},
  ...overrides,
});
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "content-type": "application/json" },
  });
const native = {
  candidates: [{ content: { role: "model", parts: [{ text: "done" }] }, finishReason: "STOP" }],
  usageMetadata: { promptTokenCount: 5001, candidatesTokenCount: 2, cachedContentTokenCount: 5000 },
};

Deno.test("Paid cache creates only eligible stable prefix; reuse includes all uncached tail messages", async () => {
  const calls: { url: string; body: Record<string, unknown> }[] = [];
  const fetcher: typeof fetch = (url, init) => {
    calls.push({ url: String(url), body: JSON.parse(String(init?.body)) });
    return Promise.resolve(response(
      String(url).endsWith(":countTokens") ? { totalTokens: 5000 } : {
        name: "cachedContents/mock",
        expireTime: new Date(Date.now() + 900_000).toISOString(),
      },
    ));
  };
  const cached = await explicitCache(request(), "fake-key", fetcher);
  assert(cached);
  assertEquals(cached.tailStart, 1);
  assertEquals(calls.length, 2);
  assertEquals(
    (calls[0]!.body.generateContentRequest as Record<string, unknown>).model,
    "models/gemini-3-flash-preview",
  );
  assertEquals(calls[1]!.body.ttl, "900s");
  const reused = await explicitCache(
    request({
      messages: [message(1), message(2), message(3), message(4)],
      newMessages: [message(4)],
      state: cached.state,
    }),
    "fake-key",
    fetcher,
  );
  assertEquals(reused?.tailStart, 1);
  assertEquals(calls.length, 2);
});

Deno.test("Cache is not reused after prompt, model, tools or media change", async () => {
  let calls = 0;
  const fetcher: typeof fetch = (url) => {
    calls++;
    return Promise.resolve(response(
      String(url).endsWith(":countTokens") ? { totalTokens: 5000 } : {
        name: `cachedContents/${calls}`,
        expireTime: new Date(Date.now() + 900_000).toISOString(),
      },
    ));
  };
  const original = await explicitCache(request(), "fake-key", fetcher);
  assert(original);
  for (
    const change of [
      { systemPrompt: "changed" },
      { model: "gemini-3.7-flash" },
      { allowCodeExecution: false },
      {
        messages: [
          message(1, {
            media: [{ kind: "image", mimeType: "image/png", bytes: new Uint8Array([1, 2, 3]) }],
          }),
          message(2),
        ],
      },
    ]
  ) {
    const rebuilt = await explicitCache(
      request({ state: original.state, ...change }),
      "fake-key",
      fetcher,
    );
    assert(rebuilt);
    assert(rebuilt.state.geminiCacheFingerprint !== original.state.geminiCacheFingerprint);
  }
  assertEquals(calls, 10);
});

Deno.test("Cache eligibility blocks other providers, fresh/small history, lossy and interaction payloads", async () => {
  let calls = 0;
  const fetcher: typeof fetch = () => {
    calls++;
    return Promise.resolve(response({ totalTokens: 20 }));
  };
  const cases: Partial<AiRequest>[] = [
    { provider: "cliproxyapi" },
    { provider: "deepseek" },
    { explicitCache: false },
    { historyRebuildLossy: true },
    { messages: [message(1)], newMessages: [message(1)] },
    { messages: [message(1, { sentAt: Math.floor(Date.now() / 1000) }), message(2)] },
    {
      messages: [
        message(1, {
          role: "model",
          assistantPayloadFormat: "gemini-interaction-steps-v1",
          assistantPayload: new TextEncoder().encode("[]"),
        }),
        message(2),
      ],
    },
  ];
  for (const item of cases) {
    assertEquals(await explicitCache(request(item), "fake-key", fetcher), undefined);
  }
  assertEquals(calls, 0);
  assertEquals(await explicitCache(request(), "fake-key", fetcher), undefined);
  assertEquals(calls, 1);
});

Deno.test("Cache-compatible SDK native history retains the signed raw parts", async () => {
  const signed = { role: "model", parts: [{ text: "old", thoughtSignature: "signed" }] };
  let counted: Record<string, unknown> = {};
  await explicitCache(
    request({
      messages: [
        message(1, {
          role: "model",
          assistantPayloadFormat: SDK_PAYLOAD_FORMAT,
          assistantPayload: new TextEncoder().encode(
            JSON.stringify({
              provider: "gemini",
              mode: "generateContent",
              native: signed,
              messages: [],
            }),
          ),
        }),
        message(2),
      ],
    }),
    "fake-key",
    (_url, init) => {
      counted = JSON.parse(String(init?.body));
      return Promise.resolve(response({ totalTokens: 20 }));
    },
  );
  assertEquals(
    ((counted.generateContentRequest as Record<string, unknown>).contents as unknown[])[0],
    signed,
  );
});

Deno.test("SDK generation sends cache reference without duplicate system/tools and recovers stale cache", async () => {
  const config = parseConfig("database-path: /tmp/unused.db\ngemini-key: fake-key\n");
  const calls: { url: string; body: Record<string, unknown> }[] = [];
  let stale = false;
  const generate = createAiGenerator(() => config, (url, init) => {
    const body = JSON.parse(String(init?.body));
    calls.push({ url: String(url), body });
    return Promise.resolve(
      response(
        String(url).endsWith(":countTokens")
          ? { totalTokens: 5000 }
          : String(url).endsWith("/cachedContents")
          ? {
            name: "cachedContents/mock",
            expireTime: new Date(Date.now() + 900_000).toISOString(),
          }
          : stale && body.cachedContent
          ? { error: { message: "expired" } }
          : native,
        stale && body.cachedContent ? 404 : 200,
      ),
    );
  });
  const first = await generate(request());
  assertEquals(first.usage.cachedInputTokens, 5000);
  const payload = calls[2]!.body;
  assertEquals(payload.cachedContent, "cachedContents/mock");
  assertEquals((payload.contents as unknown[]).length, 1);
  assertEquals(payload.tools, undefined);
  assertEquals(payload.systemInstruction, undefined);
  stale = true;
  const recovered = await generate(request({ state: first.state }));
  assertEquals(calls.length, 5);
  assertEquals(calls[3]!.body.cachedContent, "cachedContents/mock");
  assertEquals(calls[4]!.body.cachedContent, undefined);
  assertEquals((calls[4]!.body.contents as unknown[]).length, 2);
  assertEquals(recovered.state, {});
});

Deno.test("Unsupported cache creation falls back without retry; authentication failures surface", async () => {
  const unsupported: typeof fetch = () =>
    Promise.resolve(response({ error: { message: "unsupported" } }, 400));
  assertEquals(await explicitCache(request(), "fake-key", unsupported), undefined);
  await assertRejects(
    () => explicitCache(request(), "fake-key", () => Promise.resolve(response({}, 401))),
    Error,
    "HTTP 401",
  );
});

Deno.test("Argon never calls paid explicit cache or countTokens endpoints", async () => {
  const result = await explicitCache(request({ model: "gemini-4-argon" }), "fake", () => {
    throw new Error("unsupported explicit cache must never call API");
  });
  assertEquals(result, undefined);
});
