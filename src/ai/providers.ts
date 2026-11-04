import { APICallError, generateText } from "ai";
import { createGoogle } from "@ai-sdk/google";
import { createDeepSeek } from "@ai-sdk/deepseek";
import { AnthropicLanguageModel, anthropicTools } from "@ai-sdk/anthropic/internal";
import type { AppConfig } from "../config.ts";
import type { AiRequest, AiResult, ProviderState } from "./types.ts";
import {
  MODEL_DEEPSEEK_FLASH,
  MODEL_DEEPSEEK_PRO,
  MODEL_GEMINI_37_FLASH,
  MODEL_GEMINI_ARGON,
  MODEL_GEMINI_FLASH,
} from "./types.ts";
import {
  type ApiMode,
  linkedGeminiInteraction,
  modelMessages,
  replayLegacy,
  savedPayload,
  SDK_PAYLOAD_FORMAT,
} from "./messages.ts";
import { normalizeDeepseekSearchErrors } from "./deepseek_response.ts";
import { explicitCache } from "./google_cache.ts";
import { systemClock } from "../shared/types.ts";
import { latencyStage, latencySyncStage, traceMeasurements, traceTags } from "../shared/latency.ts";

const encoder = new TextEncoder();
const bytes = (value: unknown) => encoder.encode(JSON.stringify(value));
const compatibleError = (error: unknown) =>
  APICallError.isInstance(error) && [400, 404, 501].includes(error.statusCode ?? 0);

/** One entry point, direct SDK model instances; application state stays in the Run transaction. */
export function createAiGenerator(
  config: () => AppConfig,
  fetcher: typeof fetch = fetch,
  sleep = systemClock.sleep,
) {
  return async (input: AiRequest): Promise<AiResult> => {
    input.signal?.throwIfAborted();
    let request: AiRequest = {
      ...input,
      signal: input.signal
        ? AbortSignal.any([input.signal, AbortSignal.timeout(15 * 60_000)])
        : AbortSignal.timeout(15 * 60_000),
    };
    const settings = config();
    if (!["gemini", "sub2api", "deepseek"].includes(request.provider)) {
      throw new Error(`unsupported AI provider: ${request.provider}`);
    }
    const key = request.provider === "deepseek"
      ? settings.deepSeekKey
      : request.provider === "sub2api"
      ? settings.sub2apiKey
      : settings.geminiKey;
    if (!key) throw new Error(`${request.provider} API Key 未配置`);
    const last = request.messages.at(-1);
    if (
      request.provider === "deepseek" && !last?.text &&
      (last?.messageType === "video" || last?.messageType === "animation")
    ) {
      throw new Error("当前模型不处理视频");
    }

    const callOnce = async (
      mode: ApiMode,
      source = request.messages,
      state: ProviderState = {},
      previousInteractionId?: string,
    ): Promise<AiResult> => {
      let raw = new Uint8Array();
      const capture: typeof fetch = async (url, init) => {
        const body = latencySyncStage(
          "provider.request.parse",
          () => JSON.parse(String(init?.body)),
        ) as Record<string, unknown>;
        latencySyncStage(
          "provider.history.replay",
          () => replayLegacy(body, source, mode, request.historyRebuildLossy),
        );
        const encoded = latencySyncStage("provider.request.stringify", () => JSON.stringify(body));
        traceMeasurements({ request_body_chars: encoded.length });
        const response = await latencyStage(
          "provider.headers",
          () => fetcher(url, { ...init, body: encoded }),
        );
        // Preserve original bytes, including fields the SDK does not understand.
        raw = new Uint8Array(
          await latencyStage("provider.body", () => response.clone().arrayBuffer()),
        );
        traceMeasurements({ response_body_bytes: raw.byteLength });
        if (request.provider === "deepseek" && mode === "deepseek-anthropic" && response.ok) {
          const normalized = latencySyncStage("provider.response.compatibility", () => {
            let payload: unknown;
            try {
              payload = JSON.parse(new TextDecoder().decode(raw));
            } catch {
              // Leave malformed JSON to the SDK's normal error handling.
              return undefined;
            }
            return normalizeDeepseekSearchErrors(payload) ? JSON.stringify(payload) : undefined;
          });
          if (normalized !== undefined) {
            const headers = new Headers(response.headers);
            // Fetch has already decoded the body; these headers describe the original bytes.
            for (const name of ["content-length", "content-encoding", "transfer-encoding"]) {
              headers.delete(name);
            }
            await response.body?.cancel();
            return new Response(normalized, {
              status: response.status,
              statusText: response.statusText,
              headers,
            });
          }
        }
        return response;
      };
      const google = createGoogle({
        apiKey: key,
        fetch: capture,
        ...(request.provider === "sub2api" ? { baseURL: settings.sub2apiBaseUrl } : {}),
        ...(mode === "interactions" ? { headers: { "Api-Revision": "2026-05-20" } } : {}),
      });
      const deepseekNative = mode === "deepseek-anthropic";
      // Direct exported SDK instance avoids the default factory's import-time environment reads.
      const model = deepseekNative
        ? new AnthropicLanguageModel(request.model, {
          provider: "deepseek.anthropic",
          baseURL: settings.deepSeekBaseUrl.replace(/\/v1$/, "") + "/anthropic/v1",
          headers: {
            "x-api-key": key,
            authorization: `Bearer ${key}`,
            "anthropic-version": "2023-06-01",
          },
          fetch: capture,
        })
        : mode === "deepseek"
        ? createDeepSeek({ apiKey: key, baseURL: settings.deepSeekBaseUrl, fetch: capture })(
          request.model,
        )
        : mode === "interactions"
        ? google.interactions(request.model)
        : google(
          request.provider === "sub2api" && request.model === MODEL_GEMINI_37_FLASH
            ? "gemini-3.7-flash-tiered"
            : request.model,
        );
      const result = await generateText({
        model,
        messages: latencySyncStage("provider.history.build", () =>
          modelMessages(
            source,
            request.provider,
            request.model,
            mode,
            request.historyRebuildLossy,
          )),
        system: state.geminiCacheName ? undefined : request.systemPrompt,
        // Retry budget belongs to this request, shared across protocol/state recovery.
        maxRetries: 0,
        abortSignal: request.signal,
        include: { responseBody: true },
        maxOutputTokens: deepseekNative ? 32768 : undefined,
        tools: deepseekNative
          ? { web_search: anthropicTools.webSearch_20250305({ maxUses: 3 }) }
          : mode === "deepseek" || state.geminiCacheName
          ? undefined
          : {
            google_search: google.tools.googleSearch({}),
            ...(request.allowCodeExecution
              ? { code_execution: google.tools.codeExecution({}) }
              : {}),
          },
        providerOptions: deepseekNative
          ? undefined
          : mode === "deepseek"
          ? { deepseek: { thinking: { type: "enabled" } } }
          : {
            google: mode === "interactions"
              ? {
                store: true,
                previousInteractionId,
                thinkingSummaries: "auto",
                ...(request.model === MODEL_GEMINI_ARGON
                  ? { thinkingLevel: "medium" }
                  : request.model === MODEL_GEMINI_FLASH
                  ? { thinkingLevel: "low" }
                  : {}),
              }
              : {
                cachedContent: state.geminiCacheName,
                thinkingConfig: {
                  includeThoughts: true,
                  ...(request.model === MODEL_GEMINI_ARGON
                    ? { thinkingLevel: "medium" }
                    : request.model === MODEL_GEMINI_FLASH
                    ? { thinkingLevel: "low" }
                    : {}),
                },
              },
          },
      });
      const native = latencySyncStage(
        "provider.response.parse",
        () => JSON.parse(new TextDecoder().decode(raw)),
      ) as Record<string, unknown>;
      const responseModel = native.modelVersion ?? native.model;
      if (typeof responseModel === "string" && /^[\w.-]{1,120}$/.test(responseModel)) {
        traceTags({ response_model: responseModel });
      }
      const interactionId = result.providerMetadata?.google?.interactionId;
      if (mode === "interactions" && (typeof interactionId !== "string" || !interactionId)) {
        throw new Error("Gemini Interactions API did not return an interaction id");
      }
      const candidates = native.candidates as { content?: Record<string, unknown> }[] | undefined;
      const choices = native.choices as { message?: Record<string, unknown> }[] | undefined;
      const nativeAssistant = deepseekNative
        ? native.content
        : mode === "deepseek"
        ? choices?.[0]?.message
        : mode === "interactions"
        ? (native.steps as Record<string, unknown>[] | undefined)?.filter((step) =>
          step.type !== "user_input"
        )
        : candidates?.[0]?.content;
      const signature = result.response.messages.flatMap((m) =>
        m.role === "assistant" && Array.isArray(m.content) ? m.content : []
      ).map((part) =>
        "providerOptions" in part
          ? part.providerOptions?.google?.thoughtSignature ??
            part.providerOptions?.google?.signature
          : undefined
      )
        .find((value) => typeof value === "string");
      const blockReason = (native.promptFeedback as { blockReason?: string } | undefined)
        ?.blockReason;
      const assistantPayload = latencySyncStage("provider.payload.encode", () =>
        bytes({
          provider: request.provider,
          mode,
          messages: result.response.messages,
          native: nativeAssistant,
        }));
      traceMeasurements({ assistant_payload_bytes: assistantPayload.byteLength });
      return {
        displayText: deepseekNative && result.sources.length
          ? result.text + "\n\n搜索来源：\n" +
            [...new Map(
              result.sources.filter((source) =>
                source.sourceType === "url" && /^https?:\/\//.test(source.url)
              ).map((source) => [source.sourceType === "url" ? source.url : source.id, source]),
            ).values()].slice(0, 8).map((source) =>
              source.sourceType === "url"
                ? `- [${(source.title ?? source.url).replace(/[\[\]\\]/g, "\\$&").slice(0, 160)}](${
                  source.url.replace(/[()\\]/g, "\\$&")
                })`
                : ""
            ).join("\n")
          : result.text,
        assistantPayload,
        assistantPayloadFormat: SDK_PAYLOAD_FORMAT,
        usage: {
          inputTokens: result.usage.inputTokens ?? 0,
          cachedInputTokens: result.usage.inputTokenDetails.cacheReadTokens ?? 0,
          outputTokens: result.usage.outputTokens ?? 0,
          thinkingTokens: result.usage.outputTokenDetails.reasoningTokens ?? 0,
        },
        raw,
        thoughtSignature: typeof signature === "string" ? signature : undefined,
        feedback: blockReason ??
          (result.finishReason === "content-filter" ? "content-filter" : undefined),
        state: mode === "interactions"
          ? {
            geminiInteractionId: String(interactionId),
            windowStartMessageId: request.messages[0]?.messageId,
          }
          : state,
        inputMessageCount: request.messages.length,
        inputFirstMessageId: request.messages[0]?.messageId,
        inputLastMessageId: request.messages.at(-1)?.messageId,
      };
    };

    let attempts = 0;
    const call = async (...args: Parameters<typeof callOnce>): Promise<AiResult> => {
      while (attempts < 5) {
        request.signal!.throwIfAborted();
        attempts++;
        try {
          return await latencyStage(`provider.${args[0]}`, () => callOnce(...args));
        } catch (error) {
          request.signal!.throwIfAborted();
          const transient = APICallError.isInstance(error) &&
            ([408, 429].includes(error.statusCode ?? 0) ||
              (error.statusCode !== undefined && error.statusCode >= 500 &&
                error.statusCode < 600) ||
              (error.statusCode === undefined && error.isRetryable));
          if (!transient || attempts >= 5) throw error;
          await latencyStage(
            "provider.retry_backoff",
            () => sleep(3000 * 1.5 ** (attempts - 1) * (0.9 + Math.random() * 0.2), request.signal),
          );
        }
      }
      throw new Error("AI generation attempt limit reached");
    };

    if (request.provider === "deepseek") {
      return await call(
        request.model === MODEL_DEEPSEEK_FLASH || request.model === MODEL_DEEPSEEK_PRO
          ? "deepseek-anthropic"
          : "deepseek",
      );
    }
    if (request.provider === "sub2api") return await call("generateContent");
    const rebuildInteractions = async (): Promise<AiResult> => {
      try {
        return await call("interactions");
      } catch (error) {
        if (!compatibleError(error)) throw error;
        return await call("generateContent");
      }
    };
    const linked = linkedGeminiInteraction(request);
    if (linked) {
      try {
        return await call("interactions", request.newMessages, {}, linked);
      } catch (error) {
        if (!compatibleError(error) || attempts >= 5) throw error;
      }
      // Resolve the full chain only after the server explicitly rejects continuation.
      // Loading media is not a generation attempt; its failures must not become text-only replay.
      if (request.loadHistory) {
        request = { ...request, messages: await request.loadHistory(request.signal) };
      }
      request.signal!.throwIfAborted();
      return await rebuildInteractions();
    }
    // Any non-linked route (including explicit cache) needs complete local history.
    if (request.loadHistory) {
      request = { ...request, messages: await request.loadHistory(request.signal) };
    }
    const cache = await latencyStage("ai.cache", () => explicitCache(request, key, fetcher));
    if (cache) {
      try {
        return await call("generateContent", request.messages.slice(cache.tailStart), cache.state);
      } catch (error) {
        if (!compatibleError(error)) throw error;
      }
    }
    // New cache-enabled sessions use native generation so their signed payload is cacheable.
    // Existing remote Interactions chains retain that protocol and are never cached lossily.
    const interactionHistory = request.messages.some((message) =>
      message.assistantPayloadFormat === "gemini-interaction-steps-v1" ||
      (message.assistantPayloadFormat === SDK_PAYLOAD_FORMAT &&
        (savedPayload(message) as { mode?: string }).mode === "interactions")
    );
    if (
      request.explicitCache && request.model !== MODEL_GEMINI_ARGON &&
      !interactionHistory
    ) {
      return await call("generateContent");
    }
    return await rebuildInteractions();
  };
}
