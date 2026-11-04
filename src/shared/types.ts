export interface Clock {
  now(): Date;
  sleep(milliseconds: number, signal?: AbortSignal): Promise<void>;
}

export const systemClock: Clock = {
  now: () => new Date(),
  sleep: (milliseconds, signal) =>
    new Promise((resolve, reject) => {
      if (signal?.aborted) return reject(signal.reason);
      const done = () => {
        signal?.removeEventListener("abort", abort);
        resolve();
      };
      const timer = setTimeout(done, milliseconds);
      const abort = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        reject(signal?.reason ?? new DOMException("aborted", "AbortError"));
      };
      signal?.addEventListener("abort", abort, { once: true });
    }),
};

export interface ProcessResult {
  readonly code: number;
  readonly stdout: Uint8Array;
  readonly stderr: Uint8Array;
}

export interface ProcessRunner {
  run(command: string, args: readonly string[], options: {
    cwd?: string;
    timeoutMs: number;
    signal?: AbortSignal;
  }): Promise<ProcessResult>;
}

export interface TelegramFile {
  readonly fileId: string;
  readonly fileUniqueId?: string;
  readonly filePath?: string;
  readonly byteSize?: number;
}

export interface SentMessage {
  readonly chatId: number;
  readonly messageId: number;
  readonly raw: unknown;
}

export type UploadSource = string | Uint8Array | { readonly path: string };

export interface TelegramPort {
  sendTyping(chatId: number, topicId?: number, signal?: AbortSignal): Promise<void>;
  sendMessage(
    chatId: number,
    text: string,
    options?: Record<string, unknown>,
  ): Promise<SentMessage>;
  sendPhoto(
    chatId: number,
    photo: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage>;
  sendVideo(
    chatId: number,
    video: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage>;
  sendAudio(
    chatId: number,
    audio: UploadSource,
    options?: Record<string, unknown>,
  ): Promise<SentMessage>;
  sendRichMessage(
    chatId: number,
    text: string,
    options?: Record<string, unknown>,
  ): Promise<SentMessage>;
  editReplyMarkup(chatId: number, messageId: number, markup: unknown): Promise<void>;
  answerCallbackQuery(id: string, options?: Record<string, unknown>): Promise<void>;
  deleteMessage(chatId: number, messageId: number): Promise<void>;
  setReaction(
    chatId: number,
    messageId: number,
    emoji: string,
    signal?: AbortSignal,
  ): Promise<void>;
  getFile(fileId: string, signal?: AbortSignal): Promise<TelegramFile>;
  downloadFile(file: TelegramFile, signal?: AbortSignal): Promise<Uint8Array>;
}

export interface MediaObject {
  readonly fileUniqueId: string;
  readonly fileId: string;
  readonly byteSize: number;
  readonly mimeType: string;
}

export interface MediaStore {
  put(bytes: Uint8Array, media: MediaObject): Promise<MediaObject>;
  open(fileUniqueId: string): Promise<Uint8Array>;
  verify(fileUniqueId: string): Promise<boolean>;
}
