import type { ModelMessage } from "ai";
import type { AiMessage, AiProviderName, AiRequest } from "./types.ts";
import { supportsImages } from "./types.ts";

export type ApiMode = "generateContent" | "interactions" | "deepseek" | "deepseek-anthropic";
export const SDK_PAYLOAD_FORMAT = "vercel-ai-messages-v1";
const decoder = new TextDecoder();

/** A changed local window or provider requires rebuilding rather than linking remote history. */
export function linkedGeminiInteraction(
  request: Pick<AiRequest, "provider" | "state" | "messages" | "historyRebuildLossy">,
): string | undefined {
  return request.provider === "gemini" && !request.historyRebuildLossy &&
      request.state.windowStartMessageId === request.messages[0]?.messageId
    ? request.state.geminiInteractionId
    : undefined;
}

export function savedPayload(message: AiMessage): unknown {
  if (!message.assistantPayload) return undefined;
  // Never silently discard corrupt provider history, including signatures/tool results.
  return JSON.parse(decoder.decode(message.assistantPayload));
}

export function compactText(message: AiMessage): string {
  const timestamp = new Intl.DateTimeFormat("sv-SE", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(new Date(message.sentAt * 1000));
  return `[ ${message.username} ${timestamp} ]\n${message.text ?? ""}`;
}

export function modelMessages(
  messages: readonly AiMessage[],
  provider: AiProviderName,
  model: string,
  mode: ApiMode,
  lossy: boolean,
): ModelMessage[] {
  const converted = messages.flatMap((message): ModelMessage[] => {
    if (message.role === "system") return [];
    if (message.role === "model") {
      if (!lossy && message.assistantPayloadFormat === SDK_PAYLOAD_FORMAT) {
        const payload = savedPayload(message) as {
          provider: string;
          mode: string;
          messages: ModelMessage[];
        };
        if (
          payload.provider === provider && payload.mode === mode && Array.isArray(payload.messages)
        ) {
          return payload.messages;
        }
      }
      return [{ role: "assistant", content: message.text ?? "" }];
    }
    const media = message.media.filter((item) =>
      provider !== "deepseek" ||
      (supportsImages(provider, model) && item.mimeType.startsWith("image/"))
    );
    let text = compactText(message);
    if (provider === "deepseek" && message.media.length > media.length) text += "\n[不支持的媒体]";
    return [{
      role: "user",
      content: [
        { type: "text", text },
        ...media.map((item) => ({
          type: "file" as const,
          data: item.bytes,
          mediaType: item.mimeType,
        })),
      ],
    }];
  });
  if (provider !== "deepseek" || !supportsImages(provider, model)) return converted;
  const hasImage = (message: ModelMessage) =>
    message.role === "user" &&
    Array.isArray(message.content) &&
    message.content.some((part) => part.type === "file" && part.mediaType.startsWith("image/"));
  const merged: ModelMessage[] = [];
  for (const message of converted) {
    const previous = merged.at(-1);
    if (
      message.role === "user" && previous?.role === "user" &&
      Array.isArray(message.content) && Array.isArray(previous.content) &&
      (hasImage(message) || hasImage(previous))
    ) {
      previous.content.push(...message.content);
    } else merged.push(message);
  }
  const active = merged.at(-1);
  if (active?.role === "user" && Array.isArray(active.content) && !hasImage(active)) {
    for (let index = merged.length - 2; index >= 0; index--) {
      const previous = merged[index]!;
      if (previous.role !== "user" || !Array.isArray(previous.content)) continue;
      const imageIndex = previous.content.findLastIndex((part) =>
        part.type === "file" && part.mediaType.startsWith("image/")
      );
      if (imageIndex < 0) continue;
      active.content.push(previous.content.splice(imageIndex, 1)[0]!);
      break;
    }
  }
  return merged;
}

/** One-time compatibility path for saved native history, not a parallel provider implementation. */
export function replayLegacy(
  body: Record<string, unknown>,
  messages: readonly AiMessage[],
  mode: ApiMode,
  lossy: boolean,
): void {
  if (lossy || mode === "deepseek-anthropic") return;
  const formats = {
    generateContent: "gemini-content-v1",
    interactions: "gemini-interaction-steps-v1",
    deepseek: "deepseek-message-v1",
  };
  const models = messages.filter((message) => message.role === "model");
  if (!models.some((message) => message.assistantPayloadFormat === formats[mode])) return;
  const key = mode === "interactions" ? "input" : mode === "deepseek" ? "messages" : "contents";
  let index = 0;
  body[key] = (body[key] as Record<string, unknown>[]).flatMap((part) => {
    const assistant = mode === "interactions"
      ? part.type === "model_output"
      : part.role === (mode === "deepseek" ? "assistant" : "model");
    if (!assistant) return [part];
    const message = models[index++];
    if (!message || message.assistantPayloadFormat !== formats[mode]) return [part];
    const native = savedPayload(message);
    return Array.isArray(native) ? native : [native];
  });
}

export function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

/** Explicit cache must not replace a signed/tool-bearing interaction with display text. */
export function cacheContent(message: AiMessage): Record<string, unknown> | undefined {
  if (message.role === "model") {
    const payload = savedPayload(message);
    if (message.assistantPayloadFormat === "gemini-content-v1") {
      return payload as Record<string, unknown>;
    }
    if (message.assistantPayloadFormat === SDK_PAYLOAD_FORMAT) {
      const saved = payload as { provider: string; mode: string; native: Record<string, unknown> };
      if (saved.provider === "gemini" && saved.mode === "generateContent") return saved.native;
      return undefined;
    }
    if (message.assistantPayload) return undefined;
  }
  return {
    role: message.role === "model" ? "model" : "user",
    parts: [
      { text: message.role === "user" ? compactText(message) : message.text ?? "" },
      ...message.media.map((media) => ({
        inlineData: { mimeType: media.mimeType, data: base64(media.bytes) },
      })),
    ],
  };
}
