import type { MainDatabase } from "../db/database.ts";
import type { MediaObject } from "../shared/types.ts";
import type { AiMessage, AiProviderName, AiResult, AiSession, ProviderState } from "./types.ts";
import { MODEL_GEMINI_FLASH } from "./types.ts";
import { SQL } from "../db/queries.ts";

interface SessionRow {
  id: number;
  chat_id: number;
  topic_id: number | null;
  provider: AiProviderName;
  model: string;
  history_rebuild_lossy: number;
}

export interface RunRow {
  id: number;
  session_id: number;
  status:
    | "pending"
    | "generated"
    | "delivered"
    | "delivery_failed"
    | "model_failed"
    | "persistence_failed";
  provider: AiProviderName;
  model: string;
  response_text: string | null;
  raw_payload: Uint8Array | null;
  thought_signature: string | null;
  assistant_payload: Uint8Array | null;
  assistant_payload_format: string | null;
  candidate_state_json: string | null;
  input_tokens: number | null;
  output_tokens: number | null;
  cached_input_tokens: number | null;
  input_message_count: number | null;
  input_first_msg_id: number | null;
  input_last_msg_id: number | null;
}

export interface PendingAiMessage {
  readonly message: AiMessage;
  readonly mediaObjects: readonly (MediaObject & { kind: string })[];
  readonly contextOnly: boolean;
}

export interface StoredAiMessage extends AiMessage {
  readonly contextOnly: boolean;
  readonly mediaObjects: readonly { sha256: string; mimeType: string; kind: string }[];
}

export interface AiChatSelection {
  readonly provider: AiProviderName;
  readonly model: string;
  readonly showUsage: boolean;
}

function session(row: SessionRow): AiSession {
  return {
    id: row.id,
    chatId: row.chat_id,
    topicId: row.topic_id ?? undefined,
    provider: row.provider,
    model: row.model,
    historyRebuildLossy: row.history_rebuild_lossy === 1,
  };
}

export class AiRepository {
  constructor(readonly db: MainDatabase) {}

  setSessionSuspension(sessionId: number, until: number | null | "off"): void {
    if (until === "off") this.db.run(SQL.ResumeSession, sessionId);
    else this.db.run(SQL.SetSessionSuspension, sessionId, until);
  }

  isSessionSuspended(sessionId: number, now: number): boolean {
    return this.db.get(SQL.IsSessionSuspended, sessionId, now) !== undefined;
  }

  reactionsEnabled(userId: number): boolean {
    return this.db.get<{ reactions_enabled: number }>(SQL.GetReactions, userId)
      ?.reactions_enabled !== 0;
  }

  setReactions(userId: number, enabled: boolean, now: number): void {
    this.db.run(SQL.SetReactions, userId, enabled ? 1 : 0, now);
  }

  runForRequest(chatId: number, messageId: number): RunRow | undefined {
    return this.db.get<RunRow>(SQL.RunForRequest, chatId, messageId);
  }

  freezeContext(runId: number, snapshot: string): void {
    this.db.run(SQL.FreezeRunContext, runId, snapshot);
  }

  getSelection(chatId: number): AiChatSelection {
    const row = this.db.get<{
      default_provider: AiProviderName;
      default_model: string;
      show_usage: number;
    }>(
      "SELECT default_provider,default_model,show_usage FROM ai_chat_settings WHERE chat_id=?",
      chatId,
    );
    return row
      ? {
        provider: row.default_provider,
        model: row.default_model,
        showUsage: row.show_usage === 1,
      }
      : {
        provider: "gemini",
        model: MODEL_GEMINI_FLASH,
        showUsage: false,
      };
  }

  setChatModel(chatId: number, provider: AiProviderName, model: string, now: number): void {
    this.db.run(
      `INSERT INTO ai_chat_settings(chat_id,default_provider,default_model,show_usage,updated_at)
VALUES(?,?,?,0,?) ON CONFLICT(chat_id) DO UPDATE SET
default_provider=excluded.default_provider,default_model=excluded.default_model,updated_at=excluded.updated_at`,
      chatId,
      provider,
      model,
      now,
    );
  }

  toggleUsage(chatId: number, now: number): boolean {
    const selection = this.getSelection(chatId);
    this.db.run(
      `INSERT INTO ai_chat_settings(chat_id,default_provider,default_model,show_usage,updated_at)
VALUES(?,?,?,1,?) ON CONFLICT(chat_id) DO UPDATE SET
show_usage=NOT ai_chat_settings.show_usage,updated_at=excluded.updated_at`,
      chatId,
      selection.provider,
      selection.model,
      now,
    );
    return this.getSelection(chatId).showUsage;
  }

  createSession(input: {
    chatId: number;
    topicId?: number;
    chatName: string;
    chatType: string;
    provider: AiProviderName;
    model: string;
    now: number;
  }): AiSession {
    this.db.run(
      `INSERT INTO ai_sessions(chat_id,topic_id,chat_name,chat_type,provider,model,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?)`,
      input.chatId,
      input.topicId ?? null,
      input.chatName,
      input.chatType,
      input.provider,
      input.model,
      input.now,
      input.now,
    );
    const id = this.db.get<{ id: number }>("SELECT last_insert_rowid() AS id")?.id;
    if (!id) throw new Error("create AI session returned no id");
    return this.getSession(id)!;
  }

  getSession(id: number): AiSession | undefined {
    const row = this.db.get<SessionRow>(
      `SELECT id,chat_id,topic_id,provider,model,history_rebuild_lossy
FROM ai_sessions WHERE id=?`,
      id,
    );
    return row ? session(row) : undefined;
  }

  findSessionByMessage(chatId: number, messageId: number): AiSession | undefined {
    const row = this.db.get<SessionRow>(
      `SELECT s.id,s.chat_id,s.topic_id,s.provider,s.model,s.history_rebuild_lossy
FROM ai_session_messages sm JOIN ai_sessions s ON s.id=sm.session_id
WHERE sm.chat_id=? AND sm.msg_id=? AND sm.context_only=0
ORDER BY s.updated_at DESC,sm.session_id DESC LIMIT 1`,
      chatId,
      messageId,
    );
    return row ? session(row) : undefined;
  }

  setSessionModel(sessionId: number, provider: AiProviderName, model: string, now: number): void {
    this.db.transaction(() => {
      this.db.run(
        `UPDATE ai_sessions SET provider=?,model=?,history_rebuild_lossy=1,updated_at=? WHERE id=?`,
        provider,
        model,
        now,
        sessionId,
      );
      this.db.run("DELETE FROM ai_session_provider_state WHERE session_id=?", sessionId);
    });
  }

  loadProviderState(sessionId: number): ProviderState {
    const row = this.db.get<{ state_json: string }>(
      "SELECT state_json FROM ai_session_provider_state WHERE session_id=?",
      sessionId,
    );
    if (!row) return {};
    try {
      return JSON.parse(row.state_json) as ProviderState;
    } catch {
      return {};
    }
  }

  hasMessage(sessionId: number, chatId: number, messageId: number): boolean {
    return this.db.get(SQL.SessionHasMessage, sessionId, chatId, messageId) !== undefined;
  }

  loadMessages(sessionId: number, includeContext = false): StoredAiMessage[] {
    const rows = this.db.all<Record<string, unknown>>(
      `SELECT sm.chat_id,sm.msg_id,sm.role,sm.quote_part,sm.context_only,
m.sent_at,m.user_id,m.username,m.atable_username,m.msg_type,m.text,m.reply_to_msg_id,
r.assistant_payload,r.assistant_payload_format
FROM ai_session_messages sm JOIN ai_messages m ON m.chat_id=sm.chat_id AND m.msg_id=sm.msg_id
LEFT JOIN ai_runs r ON r.response_chat_id=m.chat_id AND r.response_msg_id=m.msg_id AND r.status='delivered'
WHERE sm.session_id=? AND (sm.context_only=0 OR ?=1) ORDER BY sm.position`,
      sessionId,
      includeContext ? 1 : 0,
    );
    return rows.map((row) => {
      const chatId = Number(row.chat_id);
      const messageId = Number(row.msg_id);
      const media = this.db.all<{ sha256: string; mime_type: string; media_kind: string }>(
        `SELECT o.sha256,o.mime_type,mm.media_kind
FROM ai_message_media mm JOIN media_objects o ON o.sha256=mm.media_sha256
WHERE mm.chat_id=? AND mm.msg_id=? ORDER BY mm.ordinal`,
        chatId,
        messageId,
      );
      return {
        contextOnly: Number(row.context_only) === 1,
        chatId,
        messageId,
        sentAt: Number(row.sent_at),
        userId: Number(row.user_id),
        username: String(row.username),
        atableUsername: row.atable_username == null ? undefined : String(row.atable_username),
        role: String(row.role) as AiMessage["role"],
        messageType: String(row.msg_type),
        text: row.text == null ? undefined : String(row.text),
        replyToMessageId: row.reply_to_msg_id == null ? undefined : Number(row.reply_to_msg_id),
        quotePart: row.quote_part == null ? undefined : String(row.quote_part),
        media: [],
        assistantPayload: row.assistant_payload instanceof Uint8Array
          ? row.assistant_payload
          : undefined,
        assistantPayloadFormat: row.assistant_payload_format == null
          ? undefined
          : String(row.assistant_payload_format),
        mediaObjects: media.map((item) => ({
          sha256: item.sha256,
          mimeType: item.mime_type,
          kind: item.media_kind,
        })),
      };
    });
  }

  isLoopSuppressed(chatId: number, messageId: number): boolean {
    return this.db.get(SQL.IsLoopSuppressedRequest, chatId, messageId) !== undefined;
  }

  beginRequest(
    sessionId: number,
    input: readonly PendingAiMessage[],
    now: number,
    isBot: boolean,
  ): RunRow | undefined {
    if (input.length === 0) throw new Error("cannot start AI request without a request message");
    return this.db.transaction(() => {
      const request = input.at(-1)!.message;
      const previous = this.db.get<{ suppressed: number }>(
        SQL.GetSessionRequestOrigin,
        sessionId,
        request.chatId,
        request.messageId,
      );
      if (previous) {
        return previous.suppressed
          ? undefined
          : this.getRunByRequest(sessionId, request.chatId, request.messageId);
      }
      const last = this.db.get<{ bot_streak: number }>(SQL.LastSessionRequestOrigin, sessionId);
      const streak = isBot ? Math.min(3, (last?.bot_streak ?? 0) + 1) : 0;
      const run = streak >= 3 ? undefined : this.#beginRun(sessionId, input, now);
      if (streak >= 3) this.#saveInput(sessionId, input, now);
      this.db.run(
        SQL.SaveSessionRequestOrigin,
        sessionId,
        request.chatId,
        request.messageId,
        streak,
        streak >= 3 ? 1 : 0,
      );
      return run;
    });
  }

  #saveInput(sessionId: number, input: readonly PendingAiMessage[], now: number): void {
    let position = this.db.get<{ position: number }>(
      "SELECT CAST(COALESCE(MAX(position)+1,0) AS INTEGER) AS position FROM ai_session_messages WHERE session_id=?",
      sessionId,
    )?.position ?? 0;
    for (const pending of input) {
      const message = pending.message;
      this.db.run(
        `INSERT INTO ai_messages(chat_id,msg_id,sent_at,user_id,username,atable_username,msg_type,text,reply_to_msg_id)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(chat_id,msg_id) DO NOTHING`,
        message.chatId,
        message.messageId,
        message.sentAt,
        message.userId,
        message.username,
        message.atableUsername ?? null,
        message.messageType,
        message.text ?? null,
        message.replyToMessageId ?? null,
      );
      for (let ordinal = 0; ordinal < pending.mediaObjects.length; ordinal++) {
        const media = pending.mediaObjects[ordinal]!;
        this.db.run(
          `INSERT INTO media_objects(sha256,relative_path,byte_size,mime_type,created_at)
VALUES(?,?,?,?,?) ON CONFLICT(sha256) DO NOTHING`,
          media.sha256,
          media.relativePath,
          media.byteSize,
          media.mimeType,
          now,
        );
        this.db.run(
          `INSERT INTO ai_message_media(chat_id,msg_id,ordinal,media_sha256,media_kind)
VALUES(?,?,?,?,?) ON CONFLICT(chat_id,msg_id,ordinal) DO NOTHING`,
          message.chatId,
          message.messageId,
          ordinal,
          media.sha256,
          media.kind,
        );
      }
      this.db.run(
        `INSERT INTO ai_session_messages(session_id,position,chat_id,msg_id,role,quote_part,context_only)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id,chat_id,msg_id) DO NOTHING`,
        sessionId,
        position++,
        message.chatId,
        message.messageId,
        message.role,
        message.quotePart ?? null,
        pending.contextOnly ? 1 : 0,
      );
    }
    this.db.run("UPDATE ai_sessions SET updated_at=? WHERE id=?", now, sessionId);
  }

  beginRun(sessionId: number, input: readonly PendingAiMessage[], now: number): RunRow {
    return this.db.transaction(() => this.#beginRun(sessionId, input, now));
  }

  #beginRun(sessionId: number, input: readonly PendingAiMessage[], now: number): RunRow {
    if (input.length === 0) throw new Error("cannot start AI run without a request message");
    this.#saveInput(sessionId, input, now);
    const request = input.at(-1)!.message;
    const target = this.getSession(sessionId);
    if (!target) throw new Error(`AI session ${sessionId} vanished`);
    this.db.run(
      `INSERT INTO ai_runs(session_id,request_chat_id,request_msg_id,status,provider,model,requested_at)
VALUES(?,?,?,'pending',?,?,?) ON CONFLICT(session_id,request_chat_id,request_msg_id) DO UPDATE SET id=id`,
      sessionId,
      request.chatId,
      request.messageId,
      target.provider,
      target.model,
      now,
    );
    return this.getRunByRequest(sessionId, request.chatId, request.messageId)!;
  }

  getRunByRequest(sessionId: number, chatId: number, messageId: number): RunRow | undefined {
    return this.db.get<RunRow>(
      "SELECT * FROM ai_runs WHERE session_id=? AND request_chat_id=? AND request_msg_id=?",
      sessionId,
      chatId,
      messageId,
    );
  }

  getRun(id: number): RunRow | undefined {
    return this.db.get<RunRow>("SELECT * FROM ai_runs WHERE id=?", id);
  }

  completeRun(runId: number, result: AiResult, now: number): void {
    this.db.transaction(() => {
      const stateJson = JSON.stringify(result.state);
      const updated = this.db.raw.prepare(`UPDATE ai_runs SET status='generated',completed_at=?,
input_tokens=?,output_tokens=?,cached_input_tokens=?,input_message_count=?,input_first_msg_id=?,input_last_msg_id=?,
response_text=?,raw_payload=?,thought_signature=?,assistant_payload=?,assistant_payload_format=?,
cache_expire_at=?,candidate_state_json=?,error_code=NULL,error_message=NULL
WHERE id=? AND status='pending'`).run(
        now,
        result.usage.inputTokens,
        result.usage.outputTokens,
        result.usage.cachedInputTokens,
        result.inputMessageCount,
        result.inputFirstMessageId ?? null,
        result.inputLastMessageId ?? null,
        result.displayText || null,
        result.raw,
        result.thoughtSignature ?? null,
        result.assistantPayload,
        result.assistantPayloadFormat,
        result.state.geminiCacheExpireTime ?? null,
        stateJson,
        runId,
      );
      if (Number(updated.changes) === 1) {
        const run = this.getRun(runId)!;
        this.db.run(
          `UPDATE ai_sessions SET total_input_tokens=total_input_tokens+?,
total_output_tokens=total_output_tokens+?,total_cached_input_tokens=total_cached_input_tokens+?,updated_at=? WHERE id=?`,
          result.usage.inputTokens,
          result.usage.outputTokens,
          result.usage.cachedInputTokens,
          now,
          run.session_id,
        );
      } else {
        const status = this.getRun(runId)?.status;
        if (!status || !["generated", "delivery_failed", "delivered"].includes(status)) {
          throw new Error(`AI run ${runId} cannot complete from status ${status}`);
        }
      }
    });
  }

  failRun(
    runId: number,
    status: "delivery_failed" | "model_failed" | "persistence_failed",
    error: unknown,
    now: number,
  ): void {
    this.db.run(
      `UPDATE ai_runs SET status=?,completed_at=?,error_code=?,error_message=?
WHERE id=? AND status IN ('pending','generated','delivery_failed')`,
      status,
      now,
      error instanceof Error ? error.name : "error",
      String(error),
      runId,
    );
  }

  deliverRun(runId: number, response: AiMessage, result: AiResult, now: number): void {
    this.db.transaction(() => {
      const run = this.getRun(runId);
      if (!run) throw new Error(`AI run ${runId} does not exist`);
      const target = this.getSession(run.session_id);
      if (!target) throw new Error(`AI session ${run.session_id} does not exist`);
      const position = this.db.get<{ position: number }>(
        "SELECT CAST(COALESCE(MAX(position)+1,0) AS INTEGER) AS position FROM ai_session_messages WHERE session_id=?",
        target.id,
      )?.position ?? 0;
      this.db.run(
        `INSERT INTO ai_messages(chat_id,msg_id,sent_at,user_id,username,atable_username,msg_type,text,reply_to_msg_id)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(chat_id,msg_id) DO NOTHING`,
        response.chatId,
        response.messageId,
        response.sentAt,
        response.userId,
        response.username,
        response.atableUsername ?? null,
        response.messageType,
        response.text ?? null,
        response.replyToMessageId ?? null,
      );
      this.db.run(
        `INSERT INTO ai_session_messages(session_id,position,chat_id,msg_id,role,quote_part,context_only)
VALUES(?,?,?,?,?,'',0) ON CONFLICT(session_id,chat_id,msg_id) DO NOTHING`,
        target.id,
        position,
        response.chatId,
        response.messageId,
        "model",
      );
      const delivered = this.db.raw.prepare(
        `UPDATE ai_runs SET status='delivered',response_chat_id=?,response_msg_id=?,
error_code=NULL,error_message=NULL WHERE id=? AND status IN ('generated','delivery_failed')`,
      ).run(
        response.chatId,
        response.messageId,
        runId,
      );
      if (Number(delivered.changes) !== 1) throw new Error(`AI run ${runId} is not deliverable`);
      if (target.provider === run.provider && target.model === run.model) {
        const stateJson = run.candidate_state_json ?? JSON.stringify(result.state);
        this.db.run(
          `INSERT INTO ai_session_provider_state(session_id,provider,state_version,state_json,updated_at)
VALUES(?,?,1,?,?) ON CONFLICT(session_id) DO UPDATE SET provider=excluded.provider,
state_version=excluded.state_version,state_json=excluded.state_json,updated_at=excluded.updated_at`,
          target.id,
          run.provider,
          stateJson,
          now,
        );
        this.db.run(
          "UPDATE ai_sessions SET history_rebuild_lossy=0,updated_at=? WHERE id=?",
          now,
          target.id,
        );
      }
    });
  }

  getSystemPrompt(chatId: number, topicId: number): string | undefined {
    return this.db.get<{ prompt: string }>(
      "SELECT prompt FROM ai_system_prompts WHERE chat_id=? AND topic_id=?",
      chatId,
      topicId,
    )?.prompt;
  }

  setSystemPrompt(chatId: number, topicId: number, prompt: string, now: number): void {
    this.db.run(
      `INSERT INTO ai_system_prompts(chat_id,topic_id,prompt,updated_at) VALUES(?,?,?,?)
ON CONFLICT(chat_id,topic_id) DO UPDATE SET prompt=excluded.prompt,updated_at=excluded.updated_at`,
      chatId,
      topicId,
      prompt,
      now,
    );
  }

  resetSystemPrompt(chatId: number, topicId: number): void {
    this.db.run("DELETE FROM ai_system_prompts WHERE chat_id=? AND topic_id=?", chatId, topicId);
  }

  usageForResponse(chatId: number, messageId: number): Record<string, unknown> | undefined {
    return this.db.get<Record<string, unknown>>(
      SQL.UsageForResponse,
      chatId,
      messageId,
    );
  }
}
