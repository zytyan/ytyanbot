import { Bot, InputFile } from "grammy";
import type { Message } from "grammy/types";
import type { SentMessage, TelegramFile, TelegramPort, UploadSource } from "../shared/types.ts";
import { basename } from "node:path";
import { pathToFileURL } from "node:url";

function sent(message: Message): SentMessage {
  return { chatId: message.chat.id, messageId: message.message_id, raw: message };
}

export class GrammyTelegramGateway implements TelegramPort {
  constructor(
    readonly bot: Bot,
    readonly token: string,
    readonly apiRoot: string,
    readonly localFileRoot = "/opt/tgbots/telegram-bot-api",
  ) {}

  async sendTyping(chatId: number, topicId?: number, signal?: AbortSignal): Promise<void> {
    await this.bot.api.sendChatAction(
      chatId,
      "typing",
      topicId ? { message_thread_id: topicId } : {},
      // Grammy's web declarations use a separate AbortSignal type with the same runtime API.
      signal as unknown as Parameters<Bot["api"]["sendChatAction"]>[3],
    );
  }

  async sendMessage(
    chatId: number,
    text: string,
    options: Record<string, unknown> = {},
  ): Promise<SentMessage> {
    return sent(await this.bot.api.sendMessage(chatId, text, options));
  }

  async sendPhoto(
    chatId: number,
    photo: UploadSource,
    options: Record<string, unknown> = {},
  ): Promise<SentMessage> {
    return sent(await this.bot.api.sendPhoto(chatId, this.#input(photo), options));
  }

  async sendVideo(
    chatId: number,
    video: UploadSource,
    options: Record<string, unknown> = {},
  ): Promise<SentMessage> {
    return sent(
      await this.bot.api.sendVideo(chatId, this.#input(video), {
        supports_streaming: true,
        ...options,
      }),
    );
  }

  async sendAudio(
    chatId: number,
    audio: UploadSource,
    options: Record<string, unknown> = {},
  ): Promise<SentMessage> {
    return sent(await this.bot.api.sendAudio(chatId, this.#input(audio), options));
  }

  async sendRichMessage(
    chatId: number,
    text: string,
    options: Record<string, unknown> = {},
  ): Promise<SentMessage> {
    const payload = {
      chat_id: chatId,
      rich_message: { markdown: text },
      ...options,
    };
    const response = await fetch(`${this.apiRoot}/bot${this.token}/sendRichMessage`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(130_000),
    });
    const decoded = await response.json() as {
      ok: boolean;
      result?: Message;
      description?: string;
    };
    if (!response.ok || !decoded.ok || !decoded.result) {
      throw new Error(`Telegram sendRichMessage failed: ${decoded.description ?? response.status}`);
    }
    return sent(decoded.result);
  }

  async editReplyMarkup(chatId: number, messageId: number, markup: unknown): Promise<void> {
    await this.bot.api.editMessageReplyMarkup(chatId, messageId, { reply_markup: markup as never });
  }

  async answerCallbackQuery(id: string, options: Record<string, unknown> = {}): Promise<void> {
    await this.bot.api.answerCallbackQuery(id, options);
  }

  async deleteMessage(chatId: number, messageId: number): Promise<void> {
    await this.bot.api.deleteMessage(chatId, messageId);
  }

  async setReaction(
    chatId: number,
    messageId: number,
    emoji: string,
    signal?: AbortSignal,
  ): Promise<void> {
    await this.bot.api.setMessageReaction(
      chatId,
      messageId,
      [
        { type: "emoji", emoji: emoji as never },
      ],
      {},
      signal as unknown as Parameters<Bot["api"]["setMessageReaction"]>[4],
    );
  }

  async getFile(fileId: string, signal?: AbortSignal): Promise<TelegramFile> {
    const file = await this.bot.api.getFile(
      fileId,
      signal as unknown as Parameters<Bot["api"]["getFile"]>[1],
    );
    return {
      fileId: file.file_id,
      fileUniqueId: file.file_unique_id,
      filePath: file.file_path,
      byteSize: file.file_size,
    };
  }

  async downloadFile(file: TelegramFile, signal?: AbortSignal): Promise<Uint8Array> {
    if (!file.filePath) throw new Error("Telegram file has no path");
    signal?.throwIfAborted();
    if (file.filePath.startsWith("/")) {
      const host = new URL(this.apiRoot).hostname;
      if (!["localhost", "127.0.0.1", "[::1]"].includes(host)) {
        throw new Error("absolute file path requires local Telegram API");
      }
      const root = `${this.localFileRoot}/${this.token}/`;
      try {
        const path = await Deno.realPath(file.filePath);
        const canonicalRoot = `${await Deno.realPath(root)}/`;
        if (!path.startsWith(canonicalRoot)) throw new Error("invalid local file path");
        const stat = await Deno.stat(path);
        if (!stat.isFile || stat.size > 20 * 1024 * 1024) {
          throw new Error("invalid local file size");
        }
        const input = await Deno.open(path, { read: true });
        return await fileBytes(input.readable, signal);
      } catch {
        // The local Bot API includes the token in filesystem paths.
        throw new Error("local Telegram file unavailable or outside bot directory");
      }
    }
    const response = await fetch(`${this.apiRoot}/file/bot${this.token}/${file.filePath}`, {
      signal,
    });
    if (!response.ok) throw new Error(`Telegram file download failed: HTTP ${response.status}`);
    if (!response.body) throw new Error("empty Telegram file response");
    return await fileBytes(response.body, signal);
  }

  #input(value: UploadSource): string | InputFile {
    if (typeof value === "string") return value;
    if (value instanceof Uint8Array) return new InputFile(value);
    if (["localhost", "127.0.0.1", "[::1]"].includes(new URL(this.apiRoot).hostname)) {
      // Local Bot API reads the shared file itself. Do not wrap its URI in InputFile.
      return pathToFileURL(value.path).href;
    }
    // The Web build cannot read string paths; remote API uploads need a bounded stream.
    const bytes = (async function* () {
      using file = await Deno.open(value.path, { read: true });
      const buffer = new Uint8Array(64 * 1024);
      while (true) {
        const count = await file.read(buffer);
        if (count === null) return;
        yield buffer.slice(0, count);
      }
    })();
    return new InputFile(bytes, basename(value.path));
  }
}

async function fileBytes(
  stream: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): Promise<Uint8Array> {
  const reader = stream.getReader(), chunks: Uint8Array[] = [];
  let size = 0;
  const abort = () => {
    reader.cancel().catch(() => {});
  };
  signal?.addEventListener("abort", abort, { once: true });
  try {
    while (true) {
      signal?.throwIfAborted();
      const { value, done } = await reader.read();
      signal?.throwIfAborted();
      if (done) break;
      size += value.length;
      if (size > 20 * 1024 * 1024) throw new Error("Telegram input media exceeds 20 MiB");
      chunks.push(value);
    }
    const bytes = new Uint8Array(size);
    let position = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, position);
      position += chunk.length;
    }
    return bytes;
  } finally {
    signal?.removeEventListener("abort", abort);
    await reader.cancel();
    reader.releaseLock();
  }
}
