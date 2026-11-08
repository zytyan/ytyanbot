import { assertEquals } from "@std/assert";
import { AiRepository } from "../../src/ai/repository.ts";
import type { AiResult } from "../../src/ai/types.ts";
import { temporaryDatabase } from "./helpers.ts";

const result: AiResult = {
  displayText: "answer",
  assistantPayload: new TextEncoder().encode("{}"),
  assistantPayloadFormat: "gemini-content-v1",
  usage: { inputTokens: 100, outputTokens: 20, cachedInputTokens: 30, thinkingTokens: 5 },
  raw: new TextEncoder().encode("{}"),
  state: { geminiInteractionId: "remote" },
  inputMessageCount: 1,
  inputFirstMessageId: 10,
  inputLastMessageId: 10,
};

Deno.test("AI run completion accounts tokens exactly once and delivery is atomic", async () => {
  const temporary = await temporaryDatabase();
  try {
    const repository = new AiRepository(temporary.db);
    const session = repository.createSession({
      chatId: -100,
      chatName: "group",
      chatType: "supergroup",
      provider: "gemini",
      model: "gemini-3-flash-preview",
      now: 1,
    });
    const run = repository.beginRun(session.id, [{
      message: {
        chatId: -100,
        messageId: 10,
        sentAt: 2,
        userId: 7,
        username: "user",
        role: "user",
        messageType: "text",
        text: "question",
        media: [],
      },
      mediaObjects: [],
      contextOnly: false,
    }], 2);
    repository.completeRun(run.id, result, 3);
    repository.completeRun(run.id, result, 4);
    assertEquals(
      temporary.db.get<Record<string, number>>(
        "SELECT COALESCE(SUM(input_tokens),0) AS total_input_tokens,COALESCE(SUM(output_tokens),0) AS total_output_tokens,COALESCE(SUM(cached_input_tokens),0) AS total_cached_input_tokens FROM ai_runs WHERE session_id=?",
        session.id,
      ),
      { total_input_tokens: 100, total_output_tokens: 20, total_cached_input_tokens: 30 },
    );
    repository.deliverRun(
      run.id,
      {
        chatId: -100,
        messageId: 11,
        sentAt: 4,
        userId: 99,
        username: "bot",
        role: "model",
        messageType: "text",
        text: "answer",
        replyToMessageId: 10,
        media: [],
      },
      result,
      4,
    );
    assertEquals(repository.getRun(run.id)?.status, "delivered");
    assertEquals(repository.loadProviderState(session.id).geminiInteractionId, "remote");
    assertEquals(repository.findSessionByMessage(-100, 11)?.id, session.id);
  } finally {
    await temporary.close();
  }
});

Deno.test("failed model inputs stay stored but are excluded from subsequent context", async () => {
  const temporary = await temporaryDatabase();
  try {
    const repository = new AiRepository(temporary.db);
    const session = repository.createSession({
      chatId: -100,
      chatName: "group",
      chatType: "supergroup",
      provider: "gemini",
      model: "gemini-3-flash-preview",
      now: 1,
    });
    const begin = (messageId: number) =>
      repository.beginRun(session.id, [{
        message: {
          chatId: -100,
          messageId,
          sentAt: 2,
          userId: 7,
          username: "user",
          role: "user",
          messageType: "text",
          text: "question",
          media: [],
        },
        mediaObjects: [],
        contextOnly: false,
      }], 2);
    const good = begin(10);
    repository.completeRun(good.id, result, 3);
    const failed = begin(20);
    repository.failRun(failed.id, "model_failed", new Error("INVALID_MEDIA"), 4);
    begin(30);
    for (const includeContext of [false, true]) {
      assertEquals(
        repository.loadMessages(session.id, includeContext).map((m) => m.messageId),
        [10, 30],
      );
    }
    assertEquals(repository.hasMessage(session.id, -100, 20), true);
    assertEquals(repository.getRun(failed.id)?.status, "model_failed");
    // Delivery failures retain a successful, billable generation and its input.
    repository.failRun(good.id, "delivery_failed", new Error("Telegram unavailable"), 5);
    assertEquals(repository.loadMessages(session.id).map((m) => m.messageId), [10, 30]);
  } finally {
    await temporary.close();
  }
});
