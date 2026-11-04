import type {
  ProcessResult,
  ProcessRunner,
  SentMessage,
  TelegramFile,
  TelegramPort,
  UploadSource,
} from "../../src/shared/types.ts";
import { MainDatabase } from "../../src/db/database.ts";

export async function temporaryDatabase(): Promise<
  { db: MainDatabase; path: string; close(): Promise<void> }
> {
  const directory = await Deno.makeTempDir({ prefix: "ytyanbot-test-" });
  const path = `${directory}/main.db`;
  const db = new MainDatabase(path);
  db.initialize();
  return {
    db,
    path,
    async close() {
      db.close();
      await Deno.remove(directory, { recursive: true });
    },
  };
}

export class FakeTelegram implements TelegramPort {
  readonly calls: Array<
    { method: string; media?: UploadSource; options?: Record<string, unknown> }
  > = [];
  readonly fileBytes = new Uint8Array([1, 2, 3]);
  sendTyping(_chatId: number, topicId?: number): Promise<void> {
    this.calls.push({ method: "typing", options: topicId ? { message_thread_id: topicId } : {} });
    return Promise.resolve();
  }
  #id = 100;

  #sent(raw: unknown = {}): SentMessage {
    return { chatId: -100, messageId: this.#id++, raw };
  }

  sendMessage(
    _chatId: number,
    _text: string,
    options?: Record<string, unknown>,
  ): Promise<SentMessage> {
    this.calls.push({ method: "message", options });
    return Promise.resolve(this.#sent());
  }
  sendPhoto(
    _chatId: number,
    media: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage> {
    this.calls.push({ method: "photo", media, options });
    return Promise.resolve(this.#sent({ photo: [{ file_id: "photo-id" }] }));
  }
  sendVideo(
    _chatId: number,
    media: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage> {
    this.calls.push({ method: "video", media, options });
    const fileId = typeof media === "string" ? media : "uploaded-video";
    return Promise.resolve(this.#sent({ video: { file_id: fileId } }));
  }
  sendAudio(
    _chatId: number,
    media: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage> {
    this.calls.push({ method: "audio", media, options });
    const fileId = typeof media === "string" ? media : "uploaded-audio";
    return Promise.resolve(this.#sent({ audio: { file_id: fileId } }));
  }
  sendRichMessage(
    _chatId: number,
    _text: string,
    options?: Record<string, unknown>,
  ): Promise<SentMessage> {
    this.calls.push({ method: "rich", options });
    return Promise.resolve(this.#sent());
  }
  editReplyMarkup(): Promise<void> {
    this.calls.push({ method: "edit" });
    return Promise.resolve();
  }
  answerCallbackQuery(): Promise<void> {
    return Promise.resolve();
  }
  deleteMessage(): Promise<void> {
    return Promise.resolve();
  }
  setReaction(): Promise<void> {
    return Promise.resolve();
  }
  getFile(fileId: string): Promise<TelegramFile> {
    return Promise.resolve({ fileId, fileUniqueId: `unique-${fileId}`, filePath: fileId });
  }
  downloadFile(_file: TelegramFile, _signal?: AbortSignal): Promise<Uint8Array> {
    return Promise.resolve(this.fileBytes);
  }
}

export class FakeYoutubeRunner implements ProcessRunner {
  downloads = 0;
  arguments: readonly string[] = [];

  async run(
    command: string,
    args: readonly string[],
    options: { cwd?: string },
  ): Promise<ProcessResult> {
    if (command.endsWith("yt-dlp")) {
      this.downloads++;
      this.arguments = args;
      const path = `${options.cwd}/video.mp4`;
      await Deno.writeFile(path, new Uint8Array([0, 1, 2]));
      return {
        code: 0,
        stdout: new TextEncoder().encode(JSON.stringify(path)),
        stderr: new Uint8Array(),
      };
    }
    return { code: 0, stdout: new Uint8Array(), stderr: new Uint8Array() };
  }
}
