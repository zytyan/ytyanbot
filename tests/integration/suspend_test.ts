import { assertEquals } from "@std/assert";
import { AiRepository } from "../../src/ai/repository.ts";
import { AiService, type IncomingAiMessage } from "../../src/ai/service.ts";
import type { AiResult } from "../../src/ai/types.ts";
import { systemClock } from "../../src/shared/types.ts";
import { parseConfig } from "../../src/config.ts";
import { FakeTelegram, temporaryDatabase } from "./helpers.ts";

const result: AiResult = {
  displayText: "answer",
  raw: new Uint8Array([1]),
  assistantPayload: new Uint8Array([2]),
  assistantPayloadFormat: "test",
  usage: { inputTokens: 3, outputTokens: 2, cachedInputTokens: 0, thinkingTokens: 0 },
  state: {},
  inputMessageCount: 1,
};

Deno.test("session suspension blocks AI activity, persistence and media; expiry/off restore the same history", async () => {
  const temp = await temporaryDatabase();
  try {
    const repository = new AiRepository(temp.db);
    const telegram = new FakeTelegram();
    let now = 1000, models = 0;
    const service = new AiService(
      repository,
      () => {
        models++;
        return Promise.resolve(result);
      },
      telegram,
      {
        put() {
          throw new Error("suspended media must not be saved");
        },
        verify() {
          throw new Error("unexpected media verification");
        },
        open() {
          throw new Error("unexpected media read");
        },
      },
      () => parseConfig("database-path: unused\nai-chats: [-100]"),
      { id: 99, firstName: "bot", username: "bot" },
      { ...systemClock, now: () => new Date(now * 1000) },
    );
    const message = (messageId: number, text = "@bot"): IncomingAiMessage => ({
      chatId: -100,
      chatName: "test",
      chatType: "supergroup",
      messageId,
      userId: 7,
      username: "user",
      sentAt: now,
      text,
      media: [],
    });
    await service.handle(message(1));
    const id = service.currentSessionId(-100)!;
    service.setSessionSuspension(id, now + 10);
    const count = repository.loadMessages(id).length;
    const calls = telegram.calls.length;
    for (
      const incoming of [
        {
          ...message(2),
          reply: message(1),
          media: [{ fileId: "photo", kind: "image" as const, mimeType: "image/jpeg" }],
        },
        message(3, `@bot @${id}`),
        message(4, "@bot @last"),
        // Redelivery/deduplication also observes the target session's suspension.
        message(1),
      ]
    ) {
      assertEquals(service.isRequestSuspended(incoming), true);
      await service.handle(incoming);
    }
    assertEquals(models, 1);
    assertEquals(repository.loadMessages(id).length, count);
    assertEquals(telegram.calls.length, calls);
    assertEquals(temp.db.all("SELECT * FROM ai_runs").length, 1);
    now += 10;
    await service.handle(message(5, `@bot @${id}`));
    assertEquals(models, 2);
    service.setSessionSuspension(id, null);
    now += 86400;
    assertEquals(new AiRepository(temp.db).isSessionSuspended(id, now), true);
    // A distinct new session is unaffected; cross-chat references cannot select the suspended one.
    assertEquals(service.isRequestSuspended({ ...message(6, `@bot @${id}`), chatId: -200 }), false);
    await service.handle(message(6, "@new"));
    assertEquals(models, 3);
    service.setSessionSuspension(id, "off");
    await service.handle(message(7, `@bot @${id}`));
    assertEquals(models, 4);
    assertEquals(service.currentSessionId(-100), id);
  } finally {
    await temp.close();
  }
});

Deno.test("suspension suppresses in-flight and queued replies while retaining successful payloads and usage", async () => {
  const temp = await temporaryDatabase();
  try {
    const repository = new AiRepository(temp.db);
    const telegram = new FakeTelegram();
    const entered = Promise.withResolvers<void>();
    const generated = Promise.withResolvers<AiResult>();
    let models = 0;
    const service = new AiService(
      repository,
      () => {
        models++;
        entered.resolve();
        return generated.promise;
      },
      telegram,
      {
        put() {
          throw new Error("unexpected media");
        },
        verify() {
          throw new Error("unexpected media verification");
        },
        open() {
          throw new Error("unexpected media");
        },
      },
      () => parseConfig("database-path: unused\nai-chats: [-100]"),
      { id: 99, firstName: "bot", username: "bot" },
    );
    const first: IncomingAiMessage = {
      chatId: -100,
      chatName: "test",
      chatType: "supergroup",
      messageId: 1,
      sentAt: 1,
      userId: 7,
      username: "user",
      text: "@bot",
      media: [],
    };
    const work = service.handle(first);
    await entered.promise;
    const id = service.currentSessionId(-100)!;
    const queued = service.handle({ ...first, messageId: 2, text: `@bot @${id}` });
    service.setSessionSuspension(id, null);
    service.setSessionSuspension(id, "off");
    generated.resolve(result);
    await Promise.all([work, queued]);
    assertEquals(models, 1);
    assertEquals(telegram.calls.some((c) => c.method === "rich"), false);
    assertEquals(repository.runForRequest(-100, 2), undefined);
    const run = repository.runForRequest(-100, 1)!;
    assertEquals(run.status, "generated");
    assertEquals(run.assistant_payload, result.assistantPayload);
    assertEquals(run.assistant_payload, result.assistantPayload);
    assertEquals(run.output_tokens, 2);
    // An explicit later redelivery uses the saved response and never calls the model again.
    await service.handle(first);
    assertEquals(models, 1);
    assertEquals(repository.runForRequest(-100, 1)!.status, "delivered");
    assertEquals(
      temp.db.get<{ total_output_tokens: number }>(
        "SELECT COALESCE(SUM(output_tokens),0) AS total_output_tokens FROM ai_runs WHERE session_id=?",
        id,
      )!.total_output_tokens,
      2,
    );
  } finally {
    await temp.close();
  }
});
