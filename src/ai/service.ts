import type { AppConfig } from "../config.ts";
import { KeyedMutex } from "../shared/concurrency.ts";
import type { Clock, MediaStore, TelegramPort } from "../shared/types.ts";
import { systemClock } from "../shared/types.ts";
import type { AiRepository, PendingAiMessage, RunRow, StoredAiMessage } from "./repository.ts";
import type { AiMessage, AiRequest, AiResult, AiSession } from "./types.ts";
import { supportsImages } from "./types.ts";
import { factText, type TraditionalFact } from "../interaction.ts";
import { logger } from "../logging.ts";
import { startTyping } from "../telegram/typing.ts";
import { latencyStage, latencySyncStage, traceMeasurements, traceTags } from "../shared/latency.ts";

export const DEFAULT_SYSTEM_PROMPT = `这里是一个Telegram聊天 type:%CHAT_TYPE%,name:%CHAT_NAME%
你是一个Telegram机器人，name: %BOT_NAME% username: %BOT_USERNAME%
你会看到多轮消息。用户消息以 \`[ 显示名 YYYY-MM-DD HH:mm:ss ]\` 开头，时间为 Asia/Shanghai 时区；下一行是正文，随后可能包含媒体内容。
该紧凑消息头只用于说明发送者和时间，不要在模型输出中重复消息头。
应使用中文回复消息。
请对大家温柔一些。`;

export interface IncomingMedia {
  readonly fileId: string;
  readonly uniqueId?: string;
  readonly kind: "image" | "video" | "sticker" | "animation";
  readonly mimeType: string;
  readonly byteSize?: number;
  readonly duration?: number;
}

export interface IncomingAiMessage {
  readonly chatId: number;
  readonly chatName: string;
  readonly chatType: string;
  readonly topicId?: number;
  readonly messageId: number;
  readonly sentAt: number;
  readonly userId: number;
  readonly isBot?: boolean;
  readonly username: string;
  readonly atableUsername?: string;
  readonly text?: string;
  readonly reply?: IncomingAiMessage;
  readonly media: readonly IncomingMedia[];
  readonly facts?: readonly TraditionalFact[];
  readonly completeAlbum?: boolean;
  readonly signal?: AbortSignal;
}

export interface BotIdentity {
  readonly id: number;
  readonly firstName: string;
  readonly username: string;
}

export interface SessionRouting {
  readonly createNew: boolean;
  readonly ignoreCurrent: boolean;
  readonly explicitSessionId?: number;
}

export function parseSessionRouting(text: string): SessionRouting {
  if (text.includes("@new")) return { createNew: true, ignoreCurrent: false };
  if (text.includes("@last")) return { createNew: false, ignoreCurrent: true };
  const match = text.match(/@(\d+)/);
  return {
    createNew: false,
    ignoreCurrent: false,
    explicitSessionId: match ? Number(match[1]) : undefined,
  };
}

function topicKey(message: IncomingAiMessage): string {
  return `${message.chatId}:${message.topicId ?? 0}`;
}

function storedResult(run: RunRow): AiResult {
  let state = {};
  try {
    state = run.candidate_state_json ? JSON.parse(run.candidate_state_json) : {};
  } catch {
    // Invalid state does not prevent redelivery of already generated text.
  }
  return {
    displayText: run.response_text ?? "",
    assistantPayload: run.assistant_payload ?? new Uint8Array(),
    assistantPayloadFormat: run.assistant_payload_format ?? "",
    usage: {
      inputTokens: run.input_tokens ?? 0,
      outputTokens: run.output_tokens ?? 0,
      cachedInputTokens: run.cached_input_tokens ?? 0,
      thinkingTokens: 0,
    },
    raw: run.raw_payload ?? new Uint8Array(),
    thoughtSignature: run.thought_signature ?? undefined,
    state,
    inputMessageCount: run.input_message_count ?? 0,
    inputFirstMessageId: run.input_first_msg_id ?? undefined,
    inputLastMessageId: run.input_last_msg_id ?? undefined,
  };
}

export class AiService {
  readonly #current = new Map<string, number>();
  readonly #locks = new KeyedMutex();
  readonly #active = new Map<number, Set<() => void>>();

  constructor(
    readonly repository: AiRepository,
    readonly generate: (request: AiRequest) => Promise<AiResult>,
    readonly telegram: TelegramPort,
    readonly mediaStore: MediaStore,
    readonly config: () => AppConfig,
    readonly botIdentity: BotIdentity,
    readonly clock: Clock = systemClock,
  ) {}

  resetCurrent(chatId: number, topicId = 0): void {
    this.#current.delete(`${chatId}:${topicId}`);
  }

  currentSessionId(chatId: number, topicId = 0): number | undefined {
    return this.#current.get(`${chatId}:${topicId}`);
  }

  sessionForMessage(chatId: number, messageId: number): AiSession | undefined {
    return this.repository.findSessionByMessage(chatId, messageId);
  }

  setSessionSuspension(sessionId: number, until: number | null | "off"): void {
    this.repository.setSessionSuspension(sessionId, until);
    if (until !== "off") {
      for (const stop of this.#active.get(sessionId) ?? []) stop();
    }
  }

  isRequestSuspended(message: IncomingAiMessage): boolean {
    const previous = this.repository.runForRequest(message.chatId, message.messageId);
    const target = previous
      ? this.repository.getSession(previous.session_id)
      : this.#findSession(message, parseSessionRouting(message.text ?? ""));
    return target ? this.#isSuspended(target.id) : false;
  }

  #isSuspended(sessionId: number): boolean {
    return this.repository.isSessionSuspended(
      sessionId,
      Math.floor(this.clock.now().getTime() / 1000),
    );
  }

  startActivity(message: IncomingAiMessage): () => void {
    const controller = new AbortController();
    const signal = message.signal
      ? AbortSignal.any([message.signal, controller.signal])
      : controller.signal;
    const stopTyping = startTyping(this.telegram, message.chatId, message.topicId, signal);
    if (!signal.aborted && this.repository.reactionsEnabled(message.userId)) {
      const reactionSignal = AbortSignal.any([signal, AbortSignal.timeout(2000)]);
      void latencyStage(
        "ai.reaction",
        () => this.telegram.setReaction(message.chatId, message.messageId, "👀", reactionSignal),
      )
        .catch((error: unknown) => {
          if (signal.aborted) return;
          const details = error as {
            error_code?: number;
            parameters?: { retry_after?: number };
          } | null;
          logger("ai").warn("reaction failed", {
            chat_id: message.chatId,
            message_id: message.messageId,
            error_type: error instanceof Error ? error.name : "unknown",
            error_code: details?.error_code,
            retry_after: details?.parameters?.retry_after,
            timed_out: reactionSignal.aborted,
          });
        });
    }
    return () => {
      controller.abort();
      stopTyping();
    };
  }

  async handle(message: IncomingAiMessage, stopEarlyActivity?: () => void): Promise<void> {
    if (!this.config().aiChats.includes(message.chatId)) return;
    if (this.repository.isLoopSuppressed(message.chatId, message.messageId)) return;
    const routing = parseSessionRouting(message.text ?? "");
    const previous = this.repository.runForRequest(message.chatId, message.messageId);
    if (previous?.status === "delivered") return;
    const target = previous
      ? this.repository.getSession(previous.session_id)!
      : this.#resolveSession(message, routing);
    if (this.#isSuspended(target.id)) return;
    traceTags({ session_id: target.id, provider: target.provider, model: target.model });
    const stopActivity = stopEarlyActivity ?? this.startActivity(message);
    let suspended = false;
    const stop = () => {
      suspended = true;
      stopActivity();
    };
    const active = this.#active.get(target.id) ?? new Set<() => void>();
    this.#active.set(target.id, active);
    active.add(stop);
    try {
      await this.#locks.use(String(target.id), async () => {
        await this.#handleLocked(target, message, () => suspended || this.#isSuspended(target.id));
      }, "queue.ai_session");
    } finally {
      active.delete(stop);
      if (!active.size) this.#active.delete(target.id);
      stopActivity();
    }
  }

  #findSession(message: IncomingAiMessage, routing: SessionRouting): AiSession | undefined {
    let target: AiSession | undefined;
    if (!routing.createNew && routing.explicitSessionId) {
      const explicit = this.repository.getSession(routing.explicitSessionId);
      if (explicit?.chatId === message.chatId) target = explicit;
    }
    if (!routing.createNew && !target && message.reply) {
      const replied = this.repository.findSessionByMessage(message.chatId, message.reply.messageId);
      if (replied?.chatId === message.chatId) target = replied;
    }
    if (!routing.createNew && !target && routing.ignoreCurrent) {
      const id = this.#current.get(topicKey(message));
      if (id) target = this.repository.getSession(id);
    }
    return target;
  }

  #resolveSession(message: IncomingAiMessage, routing: SessionRouting): AiSession {
    let target = this.#findSession(message, routing);
    if (!target) {
      const selection = this.repository.getSelection(message.chatId);
      target = this.repository.createSession({
        chatId: message.chatId,
        topicId: message.topicId,
        chatName: message.chatName,
        chatType: message.chatType,
        provider: selection.provider,
        model: selection.model,
        now: Math.floor(this.clock.now().getTime() / 1000),
      });
    }
    this.#current.set(topicKey(message), target.id);
    return target;
  }

  async #handleLocked(
    initialSession: AiSession,
    incoming: IncomingAiMessage,
    suspended: () => boolean,
  ): Promise<void> {
    if (suspended()) return;
    const session = this.repository.getSession(initialSession.id) ?? initialSession;
    traceTags({ provider: session.provider, model: session.model });
    const existing = this.repository.getRunByRequest(
      session.id,
      incoming.chatId,
      incoming.messageId,
    );
    if (
      existing?.status === "delivered" ||
      this.repository.isLoopSuppressed(incoming.chatId, incoming.messageId)
    ) return;
    const pending: PendingAiMessage[] = [];
    try {
      if (
        !existing && incoming.reply &&
        incoming.reply.chatId === incoming.chatId &&
        !this.repository.hasMessage(session.id, incoming.chatId, incoming.reply.messageId)
      ) {
        pending.push(
          await latencyStage(
            "ai.media.reply",
            () => this.#prepareMessage(incoming.reply!, true, session),
          ),
        );
      }
      if (!existing) {
        pending.push(
          await latencyStage(
            "ai.media.current",
            () => this.#prepareMessage(incoming, false, session),
          ),
        );
      }
    } catch (error) {
      if (suspended()) return;
      await this.telegram.sendMessage(
        incoming.chatId,
        "无法完整读取请求中的媒体，本次未调用模型。",
        {
          reply_parameters: { message_id: incoming.messageId, allow_sending_without_reply: true },
        },
      );
      throw error;
    }
    if (suspended()) return;
    const now = Math.floor(this.clock.now().getTime() / 1000);
    const run = existing ??
      latencySyncStage(
        "ai.persist.pending",
        () => this.repository.beginRequest(session.id, pending, now, incoming.isBot ?? false),
      );
    if (!run) return;
    traceTags({ run_id: run.id });
    if (run.status === "delivered") return;
    if (run.status === "model_failed" || run.status === "persistence_failed") {
      throw new Error(`previous AI attempt failed (${run.status})`);
    }
    let result: AiResult;
    if (run.status === "generated" || run.status === "delivery_failed") {
      result = storedResult(run);
    } else {
      try {
        const loaded = await latencyStage(
          "ai.history",
          () => this.#loadMessages(session.id),
        );
        if (suspended()) return;
        const drop = Math.max(0, Math.ceil((loaded.length - 200) / 50) * 50);
        const window = loaded.slice(drop);
        const promptTemplate =
          this.repository.getSystemPrompt(incoming.chatId, incoming.topicId ?? 0) ??
            DEFAULT_SYSTEM_PROMPT;
        traceMeasurements({
          history_messages: window.length,
          history_assistant_payload_bytes: window.reduce(
            (sum, m) => sum + (m.assistantPayload?.byteLength ?? 0),
            0,
          ),
          history_media_files: window.reduce((sum, m) => sum + m.media.length, 0),
          history_media_bytes: window.reduce(
            (sum, m) => sum + m.media.reduce((n, file) => n + file.bytes.byteLength, 0),
            0,
          ),
        });
        const snapshot = latencySyncStage("ai.snapshot.json", () =>
          JSON.stringify({
            provider: session.provider,
            model: session.model,
            systemPrompt: this.#renderPrompt(promptTemplate, incoming),
            messages: window.map(({ media, ...message }) => ({
              ...message,
              media: media.map(({ bytes, ...reference }) => ({
                ...reference,
                byteSize: bytes.byteLength,
              })),
            })),
          }, (_key, value) => value instanceof Uint8Array ? { bytes: Array.from(value) } : value));
        traceMeasurements({ context_snapshot_chars: snapshot.length });
        this.repository.freezeContext(run.id, snapshot);
        result = await latencyStage("ai.generate", () =>
          this.generate({
            sessionId: session.id,
            provider: session.provider,
            model: session.model,
            systemPrompt: this.#renderPrompt(promptTemplate, incoming),
            messages: window,
            // Include stored-only bot inputs when resuming a remote Interactions chain.
            newMessages: window.slice(window.findLastIndex((item) => item.role === "model") + 1),
            historyRebuildLossy: session.historyRebuildLossy,
            allowCodeExecution: window.every((item) =>
              item.media.every((media) => !media.mimeType.startsWith("video/"))
            ),
            explicitCache: this.config().geminiExplicitCache,
            state: this.repository.loadProviderState(session.id),
            signal: incoming.signal,
          }));
        latencySyncStage(
          "ai.persist.generated",
          () =>
            this.repository.completeRun(
              run.id,
              result,
              Math.floor(this.clock.now().getTime() / 1000),
            ),
        );
        logger("ai").debug("generation completed", {
          runId: run.id,
          provider: session.provider,
          model: session.model,
        });
      } catch (error) {
        logger("ai").warn("generation failed", {
          runId: run.id,
          provider: session.provider,
          model: session.model,
        });
        this.repository.failRun(
          run.id,
          "model_failed",
          error,
          Math.floor(this.clock.now().getTime() / 1000),
        );
        if (suspended()) return;
        try {
          if (this.repository.reactionsEnabled(incoming.userId)) {
            await this.telegram.setReaction(
              incoming.chatId,
              incoming.messageId,
              "😭",
              AbortSignal.timeout(2000),
            );
          }
        } catch {
          // Best effort.
        }
        if (suspended()) return;
        await this.telegram.sendMessage(incoming.chatId, "AI 请求失败，请稍后重新发起请求。", {
          reply_parameters: { message_id: incoming.messageId },
        });
        throw error;
      }
    }
    if (suspended()) return;
    let display = result.displayText;
    if (!display) {
      display = `模型没有返回任何信息${result.feedback ? `，原因: ${result.feedback}` : ""}`;
    }
    const options: Record<string, unknown> = {
      reply_parameters: { message_id: incoming.messageId, allow_sending_without_reply: true },
    };
    if (this.repository.getSelection(incoming.chatId).showUsage) {
      options.reply_markup = {
        inline_keyboard: [[{ text: "📊 Token 用量", callback_data: "aiusage" }]],
      };
    }
    try {
      const response = await latencyStage(
        "ai.delivery",
        () => this.telegram.sendRichMessage(incoming.chatId, display, options),
      );
      latencySyncStage("ai.persist.delivered", () =>
        this.repository.deliverRun(
          run.id,
          {
            chatId: response.chatId,
            messageId: response.messageId,
            sentAt: Math.floor(this.clock.now().getTime() / 1000),
            userId: this.botIdentity.id,
            username: this.botIdentity.firstName,
            atableUsername: this.botIdentity.username,
            role: "model",
            messageType: "text",
            text: display,
            replyToMessageId: incoming.messageId,
            media: [],
          },
          result,
          Math.floor(this.clock.now().getTime() / 1000),
        ));
    } catch (error) {
      logger("ai").warn("delivery failed", { runId: run.id });
      this.repository.failRun(
        run.id,
        "delivery_failed",
        error,
        Math.floor(this.clock.now().getTime() / 1000),
      );
      throw error;
    }
  }

  async #prepareMessage(
    input: IncomingAiMessage,
    contextOnly: boolean,
    session: AiSession,
  ): Promise<PendingAiMessage> {
    const mediaObjects: Array<Awaited<ReturnType<MediaStore["put"]>> & { kind: string }> = [];
    const notes: string[] = [];
    for (const media of input.media) {
      if (
        session.provider === "deepseek" &&
        (media.kind === "video" || media.kind === "animation" ||
          !supportsImages(session.provider, session.model))
      ) {
        if (input.completeAlbum) throw new Error("当前模型无法读取完整图片组");
        notes.push("[当前模型不支持此媒体，未上传]");
        continue;
      }
      if ((media.byteSize ?? 0) > 15 * 1024 * 1024 || (media.duration ?? 0) > 240) {
        if (input.completeAlbum) throw new Error("图片组含超限媒体");
        notes.push("[媒体超过 15 MiB 或 240 秒，未上传]");
        continue;
      }
      const file = await this.telegram.getFile(media.fileId);
      const timeout = AbortSignal.timeout(10_000);
      const bytes = await this.telegram.downloadFile(
        file,
        input.signal ? AbortSignal.any([input.signal, timeout]) : timeout,
      );
      if (bytes.byteLength > 15 * 1024 * 1024) {
        if (input.completeAlbum) throw new Error("图片组含超限媒体");
        notes.push("[媒体超过 15 MiB，未上传]");
        continue;
      }
      mediaObjects.push({ ...await this.mediaStore.put(bytes, media.mimeType), kind: media.kind });
    }
    const message: AiMessage = {
      chatId: input.chatId,
      messageId: input.messageId,
      sentAt: input.sentAt,
      userId: input.userId,
      username: input.username,
      atableUsername: input.atableUsername,
      role: "user",
      messageType: input.media[0]?.kind ?? "text",
      text:
        `${input.text ?? ""}${notes.length ? "\n" + notes.join("\n") : ""}${
          factText(input.facts ?? [])
        }` || undefined,
      replyToMessageId: input.reply?.messageId,
      media: [],
    };
    return { message, mediaObjects, contextOnly };
  }

  async #loadMessages(sessionId: number): Promise<AiMessage[]> {
    // A selected reply is part of this session, including every subsequent turn.
    const all = latencySyncStage(
      "ai.history.sql",
      () => this.repository.loadMessages(sessionId, true),
    );
    const drop = Math.max(0, Math.ceil((all.length - 200) / 50) * 50);
    const stored = all.slice(drop);
    return await latencyStage(
      "ai.history.media",
      () =>
        Promise.all(stored.map(async (message: StoredAiMessage): Promise<AiMessage> => ({
          ...message,
          media: await Promise.all(message.mediaObjects.map(async (media) => ({
            sha256: media.sha256,
            kind: media.kind,
            mimeType: media.mimeType,
            bytes: await this.mediaStore.open(media.sha256),
          }))),
        }))),
    );
  }

  #renderPrompt(template: string, message: IncomingAiMessage): string {
    return template
      .replaceAll("%CHAT_TYPE%", message.chatType)
      .replaceAll("%CHAT_NAME%", message.chatName)
      .replaceAll("%CHAT_ID%", String(message.chatId))
      .replaceAll("%BOT_NAME%", this.botIdentity.firstName)
      .replaceAll("%BOT_USERNAME%", this.botIdentity.username);
  }
}
