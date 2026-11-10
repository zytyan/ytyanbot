export const MODEL_GEMINI_ARGON = "gemini-4-argon";
export const MODEL_GEMINI_CARBON = "gemini-4-carbon";
export const MODEL_GEMINI_FLASH = "gemini-3-flash-preview";
export const MODEL_SONNET_46 = "claude-sonnet-4-6";
export const MODEL_GEMINI_38_FLASH = "gemini-3.8-flash-high";
export const MODEL_GEMINI_37_FLASH = "gemini-3.7-flash";
export const MODEL_GEMINI_FLASH_LITE = "gemini-3.1-flash-lite";
export const MODEL_DEEPSEEK_FLASH = "deepseek-flash";
export const MODEL_DEEPSEEK_PRO = "deepseek-v4-pro";
export const isRetiredDeepSeekModel = (model: string) =>
  [
    "deepseek-v4-flash",
    "deepseek-v4-flash-vision-exp",
    "deepseek-v4.1-flash-expires-on-0910",
    "deepseek-chat",
    "deepseek-reasoner",
  ].includes(model);

export type AiProviderName = "gemini" | "cliproxyapi" | "deepseek";
export type AiRole = "user" | "model" | "system";

export interface AiMedia {
  readonly fileUniqueId?: string;
  readonly kind: string;
  readonly mimeType: string;
  readonly bytes: Uint8Array;
}

export interface AiMessage {
  readonly chatId: number;
  readonly messageId: number;
  readonly sentAt: number;
  readonly userId: number;
  readonly username: string;
  readonly atableUsername?: string;
  readonly role: AiRole;
  readonly messageType: string;
  readonly text?: string;
  readonly replyToMessageId?: number;
  readonly quotePart?: string;
  readonly media: readonly AiMedia[];
  readonly assistantPayload?: Uint8Array;
  readonly assistantPayloadFormat?: string;
}

export interface AiUsage {
  readonly inputTokens: number;
  readonly cachedInputTokens: number;
  readonly outputTokens: number;
  readonly thinkingTokens: number;
}

export interface ProviderState {
  readonly geminiInteractionId?: string;
  readonly windowStartMessageId?: number;
  readonly geminiCacheName?: string;
  readonly geminiCacheExpireTime?: number;
  readonly geminiCacheStartMessageId?: number;
  readonly geminiCacheEndMessageId?: number;
  readonly geminiCacheTokenCount?: number;
  readonly geminiCacheFingerprint?: string;
}

export interface AiRequest {
  readonly sessionId: number;
  readonly provider: AiProviderName;
  readonly model: string;
  readonly systemPrompt: string;
  readonly messages: readonly AiMessage[];
  readonly newMessages: readonly AiMessage[];
  // Present only when older media was deferred for a linked remote conversation.
  readonly loadHistory?: (signal?: AbortSignal) => Promise<readonly AiMessage[]>;
  readonly historyRebuildLossy: boolean;
  readonly allowCodeExecution: boolean;
  readonly explicitCache: boolean;
  readonly state: ProviderState;
  readonly signal?: AbortSignal;
}

export interface AiResult {
  readonly displayText: string;
  readonly assistantPayload: Uint8Array;
  readonly assistantPayloadFormat: string;
  readonly usage: AiUsage;
  readonly raw: Uint8Array;
  readonly thoughtSignature?: string;
  readonly feedback?: string;
  readonly state: ProviderState;
  readonly inputMessageCount: number;
  readonly inputFirstMessageId?: number;
  readonly inputLastMessageId?: number;
}

export interface AiSession {
  readonly id: number;
  readonly chatId: number;
  readonly topicId?: number;
  readonly provider: AiProviderName;
  readonly model: string;
  readonly historyRebuildLossy: boolean;
}

// aimodel3 callbacks use this stable index; append new options, never reorder this version.
export const MODEL_OPTIONS = Object.freeze([
  { model: MODEL_GEMINI_37_FLASH, label: "Gemini 3.7 Flash", provider: "gemini" as const },
  { model: MODEL_GEMINI_FLASH, label: "Gemini 3 Flash Preview", provider: "gemini" as const },
  { model: MODEL_GEMINI_FLASH_LITE, label: "Gemini 3.1 Flash-Lite", provider: "gemini" as const },
  {
    model: MODEL_DEEPSEEK_FLASH,
    label: "DeepSeek V4.1 Flash（图片/搜索）",
    provider: "deepseek" as const,
  },
  {
    model: MODEL_DEEPSEEK_FLASH,
    label: "DeepSeek V4.1 Flash（图片/搜索）",
    provider: "deepseek" as const,
  },
  {
    model: MODEL_DEEPSEEK_FLASH,
    label: "DeepSeek V4.1 Flash（图片/搜索）",
    provider: "deepseek" as const,
  },
  { model: MODEL_GEMINI_ARGON, label: "Gemini 4 Argon", provider: "gemini" as const },
  { model: MODEL_DEEPSEEK_PRO, label: "DeepSeek V4 Pro（搜索）", provider: "deepseek" as const },
  { model: MODEL_SONNET_46, label: "Claude Sonnet 4.6（CPA）", provider: "cliproxyapi" as const },
  {
    model: MODEL_GEMINI_38_FLASH,
    label: "Gemini 3.8 Flash（CPA）",
    provider: "cliproxyapi" as const,
  },
  { model: MODEL_GEMINI_CARBON, label: "Gemini 4 Carbon", provider: "gemini" as const },
]);

export function supportsImages(provider: AiProviderName, model: string): boolean {
  return provider !== "deepseek" || model === MODEL_DEEPSEEK_FLASH ||
    model === "deepseek-v4-flash-vision-exp" ||
    model === "deepseek-v4.1-flash-expires-on-0910";
}

export function parseModelCallback(data: string) {
  const match = /^aimodel3:(\d+):(\d+)$/.exec(data);
  if (!match) return undefined;
  const sessionId = Number(match[1]);
  const option = MODEL_OPTIONS[Number(match[2])];
  return Number.isSafeInteger(sessionId) && option ? { sessionId, option } : undefined;
}
