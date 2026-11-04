import { assertEquals, assertStringIncludes } from "@std/assert";
import { AiRepository } from "../../src/ai/repository.ts";
import { AiService, type IncomingAiMessage } from "../../src/ai/service.ts";
import { TelegramMediaStore } from "../../src/ai/media_store.ts";
import type { AiRequest } from "../../src/ai/types.ts";
import { parseConfig } from "../../src/config.ts";
import { FakeTelegram, temporaryDatabase } from "./helpers.ts";

Deno.test("selected first reply and its media survive continuation, restart, and a new session referencing the same message", async () => {
  const temp = await temporaryDatabase();
  const requests: AiRequest[] = [];
  const repository = new AiRepository(temp.db);
  const telegram = new FakeTelegram();
  const config = parseConfig(`database-path: ${temp.path}\nai-chats: [-100]\n`);
  const createService = () =>
    new AiService(
      repository,
      (request) => {
        requests.push(request);
        return Promise.resolve({
          displayText: "answer",
          assistantPayload: new TextEncoder().encode("{}"),
          assistantPayloadFormat: "test",
          usage: { inputTokens: 10, outputTokens: 1, cachedInputTokens: 0, thinkingTokens: 0 },
          raw: new Uint8Array(),
          state: {},
          inputMessageCount: request.messages.length,
        });
      },
      telegram,
      new TelegramMediaStore(`${temp.path}.media`),
      () => config,
      { id: 99, firstName: "bot", username: "bot" },
    );
  const message = (messageId: number, text: string): IncomingAiMessage => ({
    chatId: -100,
    chatName: "test",
    chatType: "supergroup",
    messageId,
    sentAt: messageId,
    userId: 7,
    username: "user",
    text,
    media: [],
  });
  try {
    const source = {
      ...message(1, "original question with an image"),
      media: [{ fileId: "photo", kind: "image" as const, mimeType: "image/jpeg" }],
    };
    let service = createService();
    await service.handle({ ...message(2, "@bot explain"), reply: source });
    assertEquals(requests[0]!.messages.map((m) => m.messageId), [1, 2]);
    service = createService(); // No in-memory conversation survives this boundary.
    await service.handle({ ...message(3, "continue"), reply: message(100, "answer") });
    assertEquals(requests[1]!.messages.map((m) => m.messageId), [1, 2, 100, 3]);
    assertStringIncludes(requests[1]!.messages[0]!.text!, "original question");
    assertEquals(requests[1]!.messages[0]!.media[0]!.bytes, telegram.fileBytes);
    await service.handle({ ...message(4, "again"), reply: message(101, "answer") });
    assertEquals(requests[2]!.messages.map((m) => m.messageId), [1, 2, 100, 3, 101, 4]);
    assertEquals(requests[2]!.messages.filter((m) => m.messageId === 1).length, 1);
    await service.handle({ ...message(5, "@new @bot explain again"), reply: source });
    assertEquals(requests[3]!.messages.map((m) => m.messageId), [1, 5]);
    assertEquals(requests[3]!.sessionId === requests[0]!.sessionId, false);
    await service.handle({
      ...message(6, "@new @bot reuse an actual request"),
      reply: message(2, "@bot explain"),
    });
    assertEquals(requests[4]!.messages.map((m) => m.messageId), [2, 6]);
    await service.handle({
      ...message(7, "@new @bot"),
      reply: { ...source, chatId: -200 },
    });
    assertEquals(requests[5]!.messages.map((m) => m.messageId), [7]);
  } finally {
    await temp.close();
  }
});
