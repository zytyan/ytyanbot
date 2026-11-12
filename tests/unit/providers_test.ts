import { assert, assertEquals, assertRejects } from "@std/assert";
import { createAiGenerator } from "../../src/ai/providers.ts";
import { SDK_PAYLOAD_FORMAT } from "../../src/ai/messages.ts";
import { MODEL_OPTIONS, parseModelCallback, supportsImages } from "../../src/ai/types.ts";
import type { AiMessage, AiRequest, AiResult } from "../../src/ai/types.ts";
import { parseConfig } from "../../src/config.ts";

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const user = (overrides: Partial<AiMessage> = {}): AiMessage => ({
  chatId: -1,
  messageId: 1,
  sentAt: 1_700_000_000,
  userId: 7,
  username: "测试用户",
  role: "user",
  messageType: "text",
  text: "你好",
  media: [],
  ...overrides,
});
const assistant = (result: AiResult): AiMessage =>
  user({
    messageId: 2,
    role: "model",
    text: result.displayText,
    assistantPayload: result.assistantPayload,
    assistantPayloadFormat: result.assistantPayloadFormat,
  });
const request = (messages: AiMessage[], overrides: Partial<AiRequest> = {}): AiRequest => ({
  sessionId: 9,
  provider: "gemini",
  model: "gemini-3-flash-preview",
  systemPrompt: "system",
  messages,
  newMessages: messages.slice(-1),
  historyRebuildLossy: false,
  allowCodeExecution: true,
  explicitCache: false,
  state: {},
  ...(overrides.provider === "cliproxyapi" ? { model: "gemini-3.8-flash-high" } : {}),
  ...overrides,
});
const config = parseConfig(`
database-path: /tmp/unused.db
gemini-key: mock-gemini
cliproxyapi-key: mock-cpa
cliproxyapi-base-url: https://cpa.example
deepseek-key: mock-deepseek
deepseek-base-url: https://deepseek.example
`);
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "content-type": "application/json" },
  });
const nativeResponse = () => ({
  candidates: [{
    content: { role: "model", parts: [{ text: "回答", thoughtSignature: "AQID" }] },
    finishReason: "STOP",
  }],
  usageMetadata: {
    promptTokenCount: 11,
    cachedContentTokenCount: 4,
    candidatesTokenCount: 3,
    thoughtsTokenCount: 2,
  },
  unknown_field: { must_preserve: true },
});
const interactionResponse = () => ({
  id: "interaction-2",
  status: "completed",
  steps: [{ type: "thought", signature: "AQID", summary: [{ type: "text", text: "思考" }] }, {
    type: "model_output",
    content: [{ type: "text", text: "完成" }],
  }],
  usage: {
    total_input_tokens: 20,
    total_cached_tokens: 5,
    total_output_tokens: 4,
    total_thought_tokens: 2,
  },
});
const deepseekResponse = () => ({
  id: "mock",
  created: 1,
  model: "deepseek-v4-flash",
  object: "chat.completion",
  choices: [{
    index: 0,
    message: { role: "assistant", content: "回答", reasoning_content: "思考" },
    finish_reason: "stop",
  }],
  usage: {
    prompt_tokens: 12,
    prompt_cache_hit_tokens: 4,
    completion_tokens: 7,
    completion_tokens_details: { reasoning_tokens: 3 },
    total_tokens: 19,
  },
});
type Call = { url: string; headers: Headers; body: Record<string, unknown> };
function intercepted(responder: (call: Call, index: number) => Response) {
  const calls: Call[] = [];
  const fetcher: typeof fetch = (input, init) => {
    const call = {
      url: String(input),
      headers: new Headers(init?.headers),
      body: JSON.parse(String(init?.body)),
    };
    calls.push(call);
    return Promise.resolve(responder(call, calls.length));
  };
  return { calls, generate: createAiGenerator(() => config, fetcher, () => Promise.resolve()) };
}

Deno.test("DeepSeek SDK preserves native payload, reasoning replay and usage exactly once", async () => {
  const mock = intercepted(() => json(deepseekResponse()));
  const first = await mock.generate(
    request([user()], { provider: "deepseek", model: "deepseek-v4-flash" }),
  );
  assertEquals(first.displayText, "回答");
  assertEquals(first.usage, {
    inputTokens: 12,
    cachedInputTokens: 4,
    outputTokens: 7,
    thinkingTokens: 3,
  });
  assertEquals(first.assistantPayloadFormat, SDK_PAYLOAD_FORMAT);
  assertEquals(JSON.parse(decoder.decode(first.raw)), deepseekResponse());
  await mock.generate(
    request([user(), assistant(first), user({ messageId: 3 })], {
      provider: "deepseek",
      model: "deepseek-v4-flash",
    }),
  );
  const messages = mock.calls[1]!.body.messages as Record<string, unknown>[];
  assertEquals(messages[2]!.reasoning_content, "思考");
  assertEquals(mock.calls[0]!.url, "https://deepseek.example/chat/completions");
  assertEquals(mock.calls[0]!.headers.get("authorization"), "Bearer mock-deepseek");
  assertEquals(mock.calls[0]!.body.thinking, { type: "enabled" });
});

Deno.test("DeepSeek vision receives images while text-only selection does not", async () => {
  const mock = intercepted(() => json(deepseekResponse()));
  const message = user({
    messageType: "image",
    media: [{ kind: "image", mimeType: "image/png", bytes: new Uint8Array([1, 2, 3]) }],
  });
  await mock.generate(
    request([message], { provider: "deepseek", model: "deepseek-v4-flash-vision-exp" }),
  );
  const vision = mock.calls[0]!.body.messages as { content: unknown }[];
  assertEquals(vision[1]!.content, [
    { type: "text", text: "[ 测试用户 2023-11-15 06:13:20 ]\n你好" },
    { type: "image_url", image_url: { url: "data:image/png;base64,AQID" } },
  ]);
  await mock.generate(request([message], { provider: "deepseek", model: "deepseek-v4-flash" }));
  assert(!JSON.stringify(mock.calls[1]!.body).includes("image_url"));
  await assertRejects(
    () =>
      mock.generate(
        request([user({ text: undefined, messageType: "video" })], {
          provider: "deepseek",
          model: "deepseek-v4-flash",
        }),
      ),
    Error,
    "当前模型不处理视频",
  );
  assertEquals(mock.calls.length, 2);
});

for (const geminiModel of ["gemini-3.8-flash-high", "gemini-4-hydrogen", "gemini-4-argon"]) {
  Deno.test(`CLIProxyAPI ${geminiModel} uses native route and cannot leak official credentials/cache/state`, async () => {
    const mock = intercepted(() => json(nativeResponse()));
    const first = await mock.generate(request([user()], {
      provider: "cliproxyapi",
      model: geminiModel,
      explicitCache: true,
      state: {
        geminiInteractionId: "official-id",
        geminiCacheName: "cachedContents/official",
        geminiCacheExpireTime: 4_000_000_000,
      },
    }));
    assertEquals(
      mock.calls[0]!.url,
      `https://cpa.example/v1beta/models/${geminiModel}:generateContent`,
    );
    assertEquals(mock.calls[0]!.headers.get("x-goog-api-key"), "mock-cpa");
    assert(!JSON.stringify(mock.calls[0]!.body).includes("official"));
    assertEquals(first.state, {});
    assertEquals(first.thoughtSignature, "AQID");
    assertEquals(first.usage, {
      inputTokens: 11,
      cachedInputTokens: 4,
      outputTokens: 5,
      thinkingTokens: 2,
    });
    assertEquals(JSON.parse(decoder.decode(first.raw)), nativeResponse());
    await mock.generate(
      request([user(), assistant(first), user({ messageId: 3 })], {
        provider: "cliproxyapi",
        model: geminiModel,
      }),
    );
    const contents = mock.calls[1]!.body.contents as { parts: Record<string, unknown>[] }[];
    assertEquals(contents[1]!.parts[0]!.thoughtSignature, "AQID");
    assertEquals(mock.calls.length, 2);
  });
}

for (const claudeModel of ["claude-sonnet-4-6", "claude-opus-5-5-high"]) {
  Deno.test(`CPA ${claudeModel} uses Claude Messages with isolated state, images, signed replay and usage`, async () => {
    const wire = {
      id: "msg-cpa",
      type: "message",
      role: "assistant",
      model: claudeModel,
      content: [
        { type: "thinking", thinking: "reasoning", signature: "cpa-signature" },
        { type: "text", text: "Sonnet answer" },
      ],
      stop_reason: "end_turn",
      stop_sequence: null,
      usage: { input_tokens: 12, output_tokens: 7, cache_read_input_tokens: 4 },
    };
    const mock = intercepted(() => json(wire));
    const photo = user({
      media: [{ kind: "image", mimeType: "image/png", bytes: new Uint8Array([1, 2, 3]) }],
    });
    const selection = { provider: "cliproxyapi" as const, model: claudeModel };
    const first = await mock.generate(request([photo], {
      ...selection,
      explicitCache: true,
      state: { geminiInteractionId: "official", geminiCacheName: "cachedContents/official" },
    }));
    const call = mock.calls[0]!;
    assertEquals(call.url, "https://cpa.example/v1/messages");
    assertEquals(call.headers.get("x-api-key"), "mock-cpa");
    assertEquals(call.headers.get("authorization"), "Bearer mock-cpa");
    assertEquals(call.headers.get("x-goog-api-key"), null);
    assertEquals(call.body.model, claudeModel);
    assertEquals(call.body.tools, undefined);
    assert(!JSON.stringify(call.body).includes("official"));
    assert(JSON.stringify(call.body.messages).includes('"type":"image"'));
    assertEquals(first.displayText, "Sonnet answer");
    assertEquals(first.state, {});
    assertEquals(first.usage.inputTokens, 16);
    assertEquals(first.usage.outputTokens, 7);
    assertEquals(first.usage.cachedInputTokens, 4);
    const payload = JSON.parse(decoder.decode(first.assistantPayload));
    assertEquals(payload.provider, "cliproxyapi");
    assertEquals(payload.mode, "anthropic");
    assertEquals(payload.native, wire.content);
    await mock.generate(request([photo, assistant(first), user({ messageId: 3 })], selection));
    assert(JSON.stringify(mock.calls[1]!.body.messages).includes("cpa-signature"));
    assertEquals(mock.calls.length, 2);
  });
}

Deno.test("CPA rejects removed and unlisted models before any provider request", async () => {
  const mock = intercepted(() => json(nativeResponse()));
  await assertRejects(
    () =>
      mock.generate(request([user()], {
        provider: "cliproxyapi",
        model: "gemini-3.7-flash",
      })),
    Error,
    "unsupported CLIProxyAPI model",
  );
  await assertRejects(
    () =>
      mock.generate(request([user()], {
        provider: "sub2api" as AiRequest["provider"],
      })),
    Error,
    "unsupported AI provider",
  );
  assertEquals(mock.calls.length, 0);
});

Deno.test("DeepSeek vision merges photo-followup and moves latest historical image to active user", async () => {
  const mock = intercepted(() => json(deepseekResponse()));
  const photo = user({
    text: undefined,
    messageType: "image",
    media: [{ kind: "image", mimeType: "image/png", bytes: new Uint8Array([1, 2, 3]) }],
  });
  const vision = { provider: "deepseek" as const, model: "deepseek-v4-flash-vision-exp" };
  await mock.generate(request([photo, user({ messageId: 2, text: "describe" })], vision));
  const adjacent = mock.calls[0]!.body.messages as Record<string, unknown>[];
  assertEquals(adjacent.length, 2);
  assert(JSON.stringify(adjacent[1]).includes("image_url"));
  await mock.generate(
    request([
      photo,
      user({ role: "model", messageId: 2, text: "old answer" }),
      user({ messageId: 3, text: "followup" }),
    ], vision),
  );
  const followup = mock.calls[1]!.body.messages as Record<string, unknown>[];
  assert(!JSON.stringify(followup[1]).includes("image_url"));
  assert(JSON.stringify(followup.at(-1)).includes("image_url"));
  assertEquals(photo.media.length, 1);
});

Deno.test("Gemini SDK Interactions preserves signature, state and delta continuation", async () => {
  const mock = intercepted(() => json(interactionResponse()));
  const first = await mock.generate(request([user()]));
  assertEquals(first.displayText, "完成");
  assertEquals(first.state.geminiInteractionId, "interaction-2");
  assertEquals(first.usage, {
    inputTokens: 20,
    cachedInputTokens: 5,
    outputTokens: 6,
    thinkingTokens: 2,
  });
  assertEquals(mock.calls[0]!.headers.get("Api-Revision"), "2026-05-20");
  await mock.generate(
    request([user(), assistant(first), user({ messageId: 3 })], { state: first.state }),
  );
  assertEquals(mock.calls[1]!.body.previous_interaction_id, "interaction-2");
  assertEquals((mock.calls[1]!.body.input as unknown[]).length, 1);
  await mock.generate(request([user(), assistant(first), user({ messageId: 3 })]));
  const steps = mock.calls[2]!.body.input as Record<string, unknown>[];
  assertEquals(steps.find((step) => step.type === "thought")?.signature, "AQID");
});

Deno.test("Expired Interactions retries full history once; compatible endpoint falls back to native", async () => {
  const mock = intercepted((_call, index) =>
    index <= 2 ? json({ error: { message: "unsupported" } }, 404) : json(nativeResponse())
  );
  const result = await mock.generate(
    request([user()], { state: { geminiInteractionId: "expired", windowStartMessageId: 1 } }),
  );
  assertEquals(result.displayText, "回答");
  assertEquals(mock.calls.length, 3);
  assertEquals(mock.calls[0]!.body.previous_interaction_id, "expired");
  assertEquals(mock.calls[1]!.body.previous_interaction_id, undefined);
  assert(mock.calls[2]!.url.endsWith(":generateContent"));
  assertEquals(result.state, {});
});

Deno.test("Native legacy payloads replay exactly, including unknown signed tool parts", async () => {
  const native = {
    role: "model",
    parts: [{ text: "old", thoughtSignature: "signed" }, { custom_field: { secret: "preserve" } }],
  };
  const mock = intercepted(() => json(nativeResponse()));
  await mock.generate(
    request([
      user(),
      user({
        role: "model",
        messageId: 2,
        text: "old",
        assistantPayloadFormat: "gemini-content-v1",
        assistantPayload: encoder.encode(JSON.stringify(native)),
      }),
      user({ messageId: 3 }),
    ], { provider: "cliproxyapi", model: "gemini-3.8-flash-high" }),
  );
  assertEquals((mock.calls[0]!.body.contents as unknown[])[1], native);
});

Deno.test("Legacy Interactions steps and DeepSeek reasoning replay without flattening", async () => {
  const steps = [{ type: "thought", signature: "signed", summary: [] }, {
    type: "model_output",
    content: [{ type: "text", text: "old" }],
  }, { type: "google_search_result", result: "keep" }];
  const gemini = intercepted(() => json(interactionResponse()));
  await gemini.generate(
    request([
      user(),
      user({
        role: "model",
        messageId: 2,
        text: "old",
        assistantPayloadFormat: "gemini-interaction-steps-v1",
        assistantPayload: encoder.encode(JSON.stringify(steps)),
      }),
      user({ messageId: 3 }),
    ]),
  );
  assertEquals((gemini.calls[0]!.body.input as unknown[]).slice(1, 4), steps);
  const message = {
    role: "assistant",
    content: "old",
    reasoning_content: "retain",
    unknown: "native",
  };
  const deepseek = intercepted(() => json(deepseekResponse()));
  await deepseek.generate(
    request([
      user(),
      user({
        role: "model",
        messageId: 2,
        text: "old",
        assistantPayloadFormat: "deepseek-message-v1",
        assistantPayload: encoder.encode(JSON.stringify(message)),
      }),
      user({ messageId: 3 }),
    ], { provider: "deepseek", model: "deepseek-v4-flash" }),
  );
  assertEquals((deepseek.calls[0]!.body.messages as unknown[])[2], message);
});

Deno.test("Provider/model switching does not replay another provider's opaque payload", async () => {
  const mock = intercepted(() => json(deepseekResponse()));
  await mock.generate(request([
    user(),
    user({
      role: "model",
      messageId: 2,
      text: "portable",
      assistantPayloadFormat: SDK_PAYLOAD_FORMAT,
      assistantPayload: encoder.encode(
        JSON.stringify({
          provider: "gemini",
          mode: "interactions",
          messages: [{ role: "assistant", content: "MUST_NOT_REPLAY" }],
        }),
      ),
    }),
    user({ messageId: 3 }),
  ], { provider: "deepseek", model: "deepseek-v4-flash", historyRebuildLossy: true }));
  assert(!JSON.stringify(mock.calls[0]!.body).includes("MUST_NOT_REPLAY"));
  assert(JSON.stringify(mock.calls[0]!.body).includes("portable"));
});

Deno.test("Transient errors have five bounded attempts and cancellation/missing key makes no request", async () => {
  const mock = intercepted(() => json({ error: { message: "busy" } }, 503));
  await assertRejects(() => mock.generate(request([user()])));
  assertEquals(mock.calls.length, 5);
  const abort = new AbortController();
  abort.abort(new Error("cancelled"));
  await assertRejects(
    () => mock.generate(request([user()], { signal: abort.signal })),
    Error,
    "cancelled",
  );
  assertEquals(mock.calls.length, 5);
  await assertRejects(
    () =>
      createAiGenerator(() => ({ ...config, cliproxyapiKey: "" }))(
        request([user()], { provider: "cliproxyapi" }),
      ),
    Error,
    "API Key 未配置",
  );
});

Deno.test("Corrupt legacy payload is rejected rather than dropping its signatures", async () => {
  const mock = intercepted(() => json(nativeResponse()));
  await assertRejects(() =>
    mock.generate(
      request([
        user(),
        user({
          role: "model",
          assistantPayloadFormat: "gemini-content-v1",
          assistantPayload: encoder.encode("{bad"),
        }),
        user({ messageId: 3 }),
      ], { provider: "cliproxyapi" }),
    )
  );
  assertEquals(mock.calls.length, 0);
});

Deno.test("transient recovery returns successful usage once and terminal errors do not retry", async () => {
  for (const status of [408, 429, 503]) {
    const mock = intercepted((_call, index) =>
      index === 1 ? json({ error: { message: "temporary" } }, status) : json(nativeResponse())
    );
    const result = await mock.generate(request([user()], { provider: "cliproxyapi" }));
    assertEquals(mock.calls.length, 2);
    assertEquals(result.usage.inputTokens, 11);
    assertEquals(result.usage.outputTokens, 5);
  }
  for (const status of [400, 401, 403, 404]) {
    const mock = intercepted(() => json({ error: { message: "terminal" } }, status));
    await assertRejects(() => mock.generate(request([user()], { provider: "cliproxyapi" })));
    assertEquals(mock.calls.length, 1);
  }
});

Deno.test("cancelling retry backoff sends no further generation attempts", async () => {
  const abort = new AbortController();
  let calls = 0, waits = 0;
  const generate = createAiGenerator(() => config, () => {
    calls++;
    return Promise.resolve(json({ error: { message: "busy" } }, 503));
  }, (_delay, signal) => {
    waits++;
    abort.abort(new Error("cancelled during backoff"));
    return Promise.reject(signal!.reason);
  });
  await assertRejects(
    () => generate(request([user()], { provider: "cliproxyapi", signal: abort.signal })),
    Error,
    "cancelled during backoff",
  );
  assertEquals([calls, waits], [1, 1]);
});

Deno.test("native Gemini usage includes tool-use prompts and adds reasoning only once", async () => {
  const body = nativeResponse();
  const mock = intercepted(() =>
    json({
      ...body,
      usageMetadata: {
        promptTokenCount: 5210,
        toolUsePromptTokenCount: 4194,
        candidatesTokenCount: 283,
        thoughtsTokenCount: 305,
        totalTokenCount: 9992,
      },
    })
  );
  const result = await mock.generate(request([user()], { provider: "cliproxyapi" }));
  assertEquals(result.usage.inputTokens, 9404);
  assertEquals(result.usage.outputTokens, 588);
  assertEquals(result.usage.thinkingTokens, 305);
});

Deno.test("SDK round-trips Gemini provider-executed code results without local tool execution", async () => {
  const wire = {
    ...nativeResponse(),
    candidates: [{
      content: {
        role: "model",
        parts: [
          { executableCode: { language: "PYTHON", code: "print(1)" }, thoughtSignature: "AQID" },
          { codeExecutionResult: { outcome: "OUTCOME_OK", output: "1" } },
          { text: "结果是 1" },
        ],
      },
      finishReason: "STOP",
    }],
  };
  const mock = intercepted(() => json(wire));
  const first = await mock.generate(
    request([user()], { provider: "cliproxyapi", model: "gemini-3.8-flash-high" }),
  );
  assertEquals(mock.calls.length, 1);
  await mock.generate(
    request([user(), assistant(first), user({ messageId: 3 })], {
      provider: "cliproxyapi",
      model: "gemini-3.8-flash-high",
    }),
  );
  const contents = mock.calls[1]!.body.contents as { parts: Record<string, unknown>[] }[];
  assert(contents.some((content) => content.parts.some((part) => part.executableCode)));
  assert(contents.some((content) => content.parts.some((part) => part.codeExecutionResult)));
});

Deno.test("Interactions window change and lossy rebuild never reuse a stale remote chain", async () => {
  const mock = intercepted(() => json(interactionResponse()));
  const state = { geminiInteractionId: "stale", windowStartMessageId: 999 };
  await mock.generate(request([user()], { state }));
  await mock.generate(
    request([user()], { state: { ...state, windowStartMessageId: 1 }, historyRebuildLossy: true }),
  );
  mock.calls.forEach((call) => assertEquals(call.body.previous_interaction_id, undefined));
});

Deno.test("Provider response bytes are retained verbatim, not reconstructed from SDK's parsed body", async () => {
  const raw = `\n ${JSON.stringify(nativeResponse(), null, 2)}\n`;
  const generate = createAiGenerator(
    () => config,
    () => Promise.resolve(new Response(raw, { headers: { "content-type": "application/json" } })),
  );
  const result = await generate(request([user()], { provider: "cliproxyapi" }));
  assertEquals(decoder.decode(result.raw), raw);
});

Deno.test("Gemini safety block still retains raw response, reason and billable usage", async () => {
  const wire = {
    promptFeedback: { blockReason: "SAFETY" },
    usageMetadata: { promptTokenCount: 11 },
  };
  const mock = intercepted(() => json(wire));
  const result = await mock.generate(request([user()], { provider: "cliproxyapi" }));
  assertEquals(result.displayText, "");
  assertEquals(result.feedback, "SAFETY");
  assertEquals(result.usage.inputTokens, 11);
  assertEquals(JSON.parse(decoder.decode(result.raw)), wire);
  assertEquals(mock.calls.length, 1);
});

Deno.test("Cancellation during provider call aborts the request without a retry", async () => {
  const abort = new AbortController();
  let calls = 0;
  const generate = createAiGenerator(() => config, (_url, init) => {
    calls++;
    return new Promise((_resolve, reject) => {
      init!.signal!.addEventListener("abort", () => reject(init!.signal!.reason), { once: true });
      abort.abort(new Error("cancelled in flight"));
    });
  });
  await assertRejects(
    () => generate(request([user()], { provider: "cliproxyapi", signal: abort.signal })),
    Error,
    "cancelled in flight",
  );
  assertEquals(calls, 1);
});

Deno.test("Model callbacks distinguish equal model names and stay below Telegram's byte limit", () => {
  assertEquals(parseModelCallback("aimodel3:9:11")?.option.model, "claude-opus-5-5-high");
  assertEquals(parseModelCallback("aimodel3:9:12")?.option, {
    model: "gemini-4-hydrogen",
    label: "Gemini 4 Hydrogen（CPA）",
    provider: "cliproxyapi",
  });
  assertEquals(parseModelCallback("aimodel3:9:13")?.option, {
    model: "gemini-4-argon",
    label: "Gemini 4 Argon（CPA）",
    provider: "cliproxyapi",
  });
  assertEquals(MODEL_OPTIONS[2], undefined);
  assertEquals(parseModelCallback("aimodel3:9:2"), undefined);
  MODEL_OPTIONS.forEach((option, index) => {
    if (!option) return;
    const data = `aimodel3:9007199254740991:${index}`;
    assert(encoder.encode(data).length <= 64);
    assertEquals(parseModelCallback(data), { sessionId: Number.MAX_SAFE_INTEGER, option });
  });
  const official = MODEL_OPTIONS.findIndex((m) =>
    m?.model === "gemini-3.7-flash" && m.provider === "gemini"
  );
  const proxy = MODEL_OPTIONS.findIndex((m) =>
    m?.model === "gemini-3.8-flash-high" && m.provider === "cliproxyapi"
  );
  assert(official >= 0 && proxy >= 0 && official !== proxy);
  assertEquals(
    MODEL_OPTIONS.filter((m) => m?.provider === "cliproxyapi").map((m) => [m!.model, m!.label]),
    [
      ["claude-sonnet-4-6", "Claude Sonnet 4.6（CPA）"],
      ["gemini-3.8-flash-high", "Gemini 3.8 Flash（CPA）"],
      ["claude-opus-5-5-high", "Claude Opus 5.5（CPA）"],
      ["gemini-4-hydrogen", "Gemini 4 Hydrogen（CPA）"],
      ["gemini-4-argon", "Gemini 4 Argon（CPA）"],
    ],
  );
  [
    "aimodel:0:gemini-3.8-flash-high",
    "aimodel2:0:1",
    "aimodel3:-1:0",
    "aimodel3:0:99",
    "aimodel3:9007199254740992:0",
    "aimodel3:1:0junk",
  ].forEach((data) => assertEquals(parseModelCallback(data), undefined));
});

Deno.test("Carbon menu selects official Gemini and preserves two-turn native replay", async () => {
  assertEquals(parseModelCallback("aimodel3:9:10")?.option, {
    model: "gemini-4-carbon",
    label: "Gemini 4 Carbon",
    provider: "gemini",
  });
  const mock = intercepted(() => json(nativeResponse()));
  const currentUser = user({ sentAt: Math.floor(Date.now() / 1000) });
  const first = await mock.generate(request([currentUser], {
    model: "gemini-4-carbon",
    explicitCache: true,
  }));
  await mock.generate(
    request([
      currentUser,
      { ...assistant(first), sentAt: currentUser.sentAt },
      user({ messageId: 3 }),
    ], {
      model: "gemini-4-carbon",
      explicitCache: true,
      state: first.state,
    }),
  );
  assertEquals(mock.calls.length, 2);
  for (const call of mock.calls) {
    assert(call.url.endsWith("/models/gemini-4-carbon:generateContent"));
  }
  const contents = mock.calls[1]!.body.contents as { parts: { thoughtSignature?: string }[] }[];
  assertEquals(contents.length, 3);
  assert(contents[1]!.parts.some((part) => part.thoughtSignature === "AQID"));
  assertEquals(first.usage.outputTokens, 5);
});

Deno.test("Argon is appended to model choices and uses medium Interactions despite explicit cache settings", async () => {
  const index = MODEL_OPTIONS.findIndex((option) => option?.model === "gemini-4-argon");
  assertEquals(index, 6);
  assertEquals(parseModelCallback(`aimodel3:9:${index}`)?.option.provider, "gemini");
  const mock = intercepted(() => json(interactionResponse()));
  const first = await mock.generate(request([user()], {
    model: "gemini-4-argon",
    explicitCache: true,
    state: { geminiCacheName: "old", geminiCacheExpireTime: 9999999999 },
  }));
  assertEquals(mock.calls.length, 1);
  assert(mock.calls[0]!.url.endsWith("/interactions"));
  assertEquals(mock.calls[0]!.body.model, "gemini-4-argon");
  assertEquals(
    (mock.calls[0]!.body.generation_config as Record<string, unknown>).thinking_level,
    "medium",
  );
  assertEquals(first.usage.outputTokens, 6);
  await mock.generate(request([user(), assistant(first), user({ messageId: 3 })], {
    model: "gemini-4-argon",
    explicitCache: true,
    state: first.state,
  }));
  assertEquals(mock.calls[1]!.body.previous_interaction_id, "interaction-2");
  assertEquals((mock.calls[1]!.body.input as unknown[]).length, 1);
});

Deno.test("Argon native compatibility fallback uses its real model ID and medium thinking without explicit cache", async () => {
  const mock = intercepted((_call, index) =>
    index === 1 ? json({ error: { message: "unsupported endpoint" } }, 404) : json(nativeResponse())
  );
  await mock.generate(request([user()], { model: "gemini-4-argon", explicitCache: true }));
  assertEquals(mock.calls.length, 2);
  assert(mock.calls[1]!.url.endsWith("/models/gemini-4-argon:generateContent"));
  const body = mock.calls[1]!.body;
  assertEquals(
    (body.generationConfig as { thinkingConfig: { thinkingLevel: string } }).thinkingConfig
      .thinkingLevel,
    "medium",
  );
  assertEquals(body.cachedContent, undefined);
});

const nativeDeepseekResponse = () => ({
  id: "msg-ds",
  type: "message",
  role: "assistant",
  model: "deepseek-flash",
  content: [
    { type: "thinking", thinking: "reasoning", signature: "signed-reasoning" },
    {
      type: "server_tool_use",
      id: "srvtoolu-1",
      name: "web_search",
      input: { query: "latest release" },
    },
    {
      type: "web_search_tool_result",
      tool_use_id: "srvtoolu-1",
      content: [
        {
          type: "web_search_result",
          url: "https://example.com/release",
          title: "Release",
          encrypted_content: "opaque",
          page_age: null,
        },
      ],
    },
    { type: "text", text: "Latest release", citations: null },
  ],
  stop_reason: "end_turn",
  stop_sequence: null,
  usage: {
    input_tokens: 20,
    output_tokens: 6,
    cache_read_input_tokens: 4,
    cache_creation_input_tokens: 0,
  },
});

Deno.test("latest DeepSeek native search retains server tools, sources, reasoning and usage across replies", async () => {
  const mock = intercepted(() => json(nativeDeepseekResponse()));
  const first = await mock.generate(
    request([user()], { provider: "deepseek", model: "deepseek-flash" }),
  );
  assertEquals(mock.calls[0]!.url, "https://deepseek.example/anthropic/v1/messages");
  assertEquals(mock.calls[0]!.headers.get("x-api-key"), "mock-deepseek");
  const tools = mock.calls[0]!.body.tools as { type: string; max_uses: number }[];
  assertEquals(tools[0]!.type, "web_search_20250305");
  assertEquals(tools[0]!.max_uses, 3);
  assertEquals(first.usage.inputTokens, 24);
  assertEquals(first.usage.cachedInputTokens, 4);
  assertEquals(first.usage.outputTokens, 6);
  assert(first.displayText.includes("https://example.com/release"));
  assertEquals(JSON.parse(decoder.decode(first.raw)), nativeDeepseekResponse());
  const saved = JSON.parse(decoder.decode(first.assistantPayload));
  assertEquals(saved.mode, "deepseek-anthropic");
  assertEquals(saved.native, nativeDeepseekResponse().content);
  await mock.generate(
    request([user(), assistant(first), user({ messageId: 3 })], {
      provider: "deepseek",
      model: "deepseek-flash",
    }),
  );
  assert(JSON.stringify(mock.calls[1]!.body.messages).includes("signed-reasoning"));
  assert(JSON.stringify(mock.calls[1]!.body.messages).includes("web_search_tool_result"));
});

Deno.test("latest DeepSeek imports old Chat history portably and Pro exposes the same native search", async () => {
  const mock = intercepted(() => json(nativeDeepseekResponse()));
  const old = user({
    role: "model",
    messageId: 2,
    text: "old answer",
    assistantPayloadFormat: "deepseek-message-v1",
    assistantPayload: encoder.encode(
      JSON.stringify({
        role: "assistant",
        content: "old answer",
        reasoning_content: "legacy reasoning",
      }),
    ),
  });
  await mock.generate(
    request([user(), old, user({ messageId: 3 })], {
      provider: "deepseek",
      model: "deepseek-v4-pro",
    }),
  );
  assertEquals(mock.calls[0]!.body.model, "deepseek-v4-pro");
  assert(JSON.stringify(mock.calls[0]!.body.messages).includes("old answer"));
  assertEquals(
    JSON.parse(decoder.decode(old.assistantPayload!)).reasoning_content,
    "legacy reasoning",
  );
});

Deno.test("latest Flash supports images and retired menu slots retain stable callback indexes", async () => {
  assertEquals(supportsImages("deepseek", "deepseek-flash"), true);
  assertEquals(supportsImages("deepseek", "deepseek-v4-pro"), false);
  for (const index of [3, 4, 5]) {
    assertEquals(parseModelCallback(`aimodel3:9:${index}`)?.option.model, "deepseek-flash");
  }
  assertEquals(parseModelCallback("aimodel3:9:6")?.option.model, "gemini-4-argon");
  const mock = intercepted(() => json(nativeDeepseekResponse()));
  const photo = user({
    messageType: "image",
    media: [{ kind: "image", mimeType: "image/png", bytes: new Uint8Array([1, 2, 3]) }],
  });
  await mock.generate(request([photo], { provider: "deepseek", model: "deepseek-flash" }));
  assert(JSON.stringify(mock.calls[0]!.body.messages).includes('"type":"image"'));
});

for (const code of ["max_uses_exceeded", "too_many_requests", "unavailable"]) {
  Deno.test(`DeepSeek search error array ${code} preserves answer, raw bytes, usage and continuation`, async () => {
    const base = nativeDeepseekResponse();
    const errorBlock = {
      type: "web_search_tool_result",
      tool_use_id: "srvtoolu-error",
      content: [{ type: "web_search_tool_result_error", error_code: code }],
    };
    const response = {
      ...base,
      content: [
        ...base.content.slice(0, -1),
        {
          type: "server_tool_use",
          id: "srvtoolu-error",
          name: "web_search",
          input: { query: "more" },
        },
        errorBlock,
        base.content.at(-1),
      ],
    };
    const original = `\n ${JSON.stringify(response, null, 2)}\n`;
    const mock = intercepted(() =>
      new Response(original, {
        headers: {
          "content-type": "application/json",
          "content-length": String(encoder.encode(original).length),
        },
      })
    );
    const first = await mock.generate(
      request([user()], { provider: "deepseek", model: "deepseek-flash" }),
    );
    assertEquals(mock.calls.length, 1);
    assert(first.displayText.includes("Latest release"));
    assert(first.displayText.includes("https://example.com/release"));
    assertEquals(first.usage.inputTokens, 24);
    assertEquals(first.usage.outputTokens, 6);
    assertEquals(decoder.decode(first.raw), original);
    const saved = JSON.parse(decoder.decode(first.assistantPayload));
    assertEquals(saved.native, response.content);
    assert(JSON.stringify(saved.messages).includes(code));
    await mock.generate(request([user(), assistant(first), user({ messageId: 3 })], {
      provider: "deepseek",
      model: "deepseek-flash",
    }));
    assertEquals(mock.calls.length, 2);
    const history = JSON.stringify(mock.calls[1]!.body.messages);
    assert(history.includes("signed-reasoning"));
    assert(history.includes("opaque"));
    const blocks = (mock.calls[1]!.body.messages as {
      content: { type: string; tool_use_id?: string; content?: unknown }[];
    }[])
      .flatMap((m) => m.content);
    assertEquals(
      blocks.find((b) => b.type === "web_search_tool_result" && b.tool_use_id === "srvtoolu-error")!
        .content,
      { type: "web_search_tool_result_error", error_code: code },
    );
  });
}

Deno.test("DeepSeek search errors without display text still retain successful usage and original payload", async () => {
  const response = {
    ...nativeDeepseekResponse(),
    content: [
      {
        type: "server_tool_use",
        id: "srvtoolu-error",
        name: "web_search",
        input: { query: "more" },
      },
      {
        type: "web_search_tool_result",
        tool_use_id: "srvtoolu-error",
        content: [{ type: "web_search_tool_result_error", error_code: "max_uses_exceeded" }],
      },
    ],
  };
  const mock = intercepted(() => json(response));
  const result = await mock.generate(
    request([user()], { provider: "deepseek", model: "deepseek-flash" }),
  );
  assertEquals(result.displayText, "");
  assertEquals(result.usage.inputTokens, 24);
  assertEquals(result.usage.outputTokens, 6);
  assertEquals(JSON.parse(decoder.decode(result.raw)), response);
  assertEquals(mock.calls.length, 1);
});

Deno.test("DeepSeek ambiguous mixed search arrays remain invalid rather than discarding results", async () => {
  const response = nativeDeepseekResponse();
  const mixed = {
    ...response,
    content: [
      ...response.content.slice(0, -1),
      {
        type: "web_search_tool_result",
        tool_use_id: "srvtoolu-error",
        content: [
          { type: "web_search_tool_result_error", error_code: "max_uses_exceeded" },
          {
            type: "web_search_result",
            url: "https://example.com",
            title: "result",
            encrypted_content: "opaque",
          },
        ],
      },
      response.content.at(-1),
    ],
  };
  const mock = intercepted(() => json(mixed));
  await assertRejects(
    () => mock.generate(request([user()], { provider: "deepseek", model: "deepseek-flash" })),
    Error,
    "Invalid JSON response",
  );
  assertEquals(mock.calls.length, 1);
});

Deno.test("linked Gemini retries only the delta and never resolves deferred history on transient errors", async () => {
  let loads = 0;
  const mock = intercepted((_call, index) =>
    index < 3 ? json({ error: { message: "busy" } }, 503) : json(interactionResponse())
  );
  await mock.generate(request([user(), user({ messageId: 3, text: "new tail" })], {
    newMessages: [user({ messageId: 3, text: "new tail" })],
    explicitCache: true,
    state: { geminiInteractionId: "remote", windowStartMessageId: 1 },
    loadHistory: () => {
      loads++;
      throw new Error("history must remain deferred");
    },
  }));
  assertEquals(mock.calls.length, 3);
  assertEquals(loads, 0);
  for (const call of mock.calls) {
    assertEquals(call.body.previous_interaction_id, "remote");
    assertEquals((call.body.input as unknown[]).length, 1);
    assert(JSON.stringify(call.body.input).includes("new tail"));
  }
});

Deno.test("exhausted shared attempt budget does not trigger unnecessary media reconstruction", async () => {
  let loads = 0;
  const mock = intercepted((_call, index) =>
    json({ error: { message: "unavailable" } }, index < 5 ? 503 : 404)
  );
  await assertRejects(() =>
    mock.generate(request([user()], {
      state: { geminiInteractionId: "remote", windowStartMessageId: 1 },
      loadHistory: () => {
        loads++;
        return Promise.resolve([user()]);
      },
    }))
  );
  assertEquals(mock.calls.length, 5);
  assertEquals(loads, 0);
});
