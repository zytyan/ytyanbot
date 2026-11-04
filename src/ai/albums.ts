import type { Message } from "grammy/types";
import type { MainDatabase } from "../db/database.ts";
import { SQL } from "../db/queries.ts";
import type { IncomingMedia } from "./service.ts";
import { topicId } from "../telegram/topic.ts";

const ttlSeconds = 7 * 86400;
export function albumIndex(db: MainDatabase, idleMs = 1000, maxWaitMs = 5000) {
  const activity = new Map<string, { seen: number; claimedAt?: number }>();
  const key = (message: Message, group: string) =>
    `${message.chat.id}:${topicId(message)}:${group}`;
  const groupFor = (message: Message) =>
    message.media_group_id ?? db.get<{ group_id: string }>(
      SQL.AlbumForMessage,
      message.chat.id,
      topicId(message),
      message.message_id,
    )?.group_id;
  return {
    capture(message: Message) {
      const group = message.media_group_id;
      if (!group) return;
      const photo = message.photo?.at(-1), topic = topicId(message);
      if (photo) {
        db.run(
          SQL.IndexAlbumFile,
          message.chat.id,
          topic,
          group,
          message.message_id,
          photo.file_id,
          photo.file_unique_id,
          photo.file_size ?? null,
          message.date + ttlSeconds,
          1,
        );
      } else {
        // Mixed groups still need a row carrying the group metadata for rejection.
        const file = message.video ?? message.document ?? message.animation ?? message.audio;
        if (file) {
          const item = file as { file_id: string; file_unique_id: string; file_size?: number };
          db.run(
            SQL.IndexAlbumFile,
            message.chat.id,
            topic,
            group,
            message.message_id,
            item.file_id,
            item.file_unique_id ?? "",
            item.file_size ?? null,
            message.date + ttlSeconds,
            0,
          );
        }
      }
      const id = key(message, group), previous = activity.get(id);
      activity.delete(id);
      activity.set(id, { seen: Date.now(), claimedAt: previous?.claimedAt });
      while (activity.size > 4096) activity.delete(activity.keys().next().value!);
    },
    claim(message: Message): boolean {
      const group = message.media_group_id;
      if (!group) return true;
      const id = key(message, group), previous = activity.get(id), now = Date.now();
      if (previous?.claimedAt !== undefined && now - previous.claimedAt < 60_000) return false;
      activity.set(id, { seen: previous?.seen ?? now, claimedAt: now });
      return true;
    },
    async media(message: Message, signal: AbortSignal): Promise<IncomingMedia[] | undefined> {
      const group = groupFor(message);
      if (!group) return undefined;
      const id = key(message, group), deadline = Date.now() + maxWaitMs;
      while (true) {
        signal.throwIfAborted();
        const wait = Math.min(
          (activity.get(id)?.seen ?? 0) + idleMs - Date.now(),
          deadline - Date.now(),
        );
        if (wait <= 0) break;
        await new Promise<void>((resolve, reject) => {
          const done = () => {
            signal.removeEventListener("abort", abort);
            resolve();
          };
          const timer = setTimeout(done, wait);
          const abort = () => {
            clearTimeout(timer);
            reject(signal.reason);
          };
          signal.addEventListener("abort", abort, { once: true });
          if (signal.aborted) abort();
        });
      }
      const metadata = db.get<{ topic_id: number; expires_at: number; photo_only: number }>(
        SQL.GetAlbum,
        message.chat.id,
        topicId(message),
        group,
      );
      if (!metadata || metadata.expires_at <= Math.floor(Date.now() / 1000)) {
        throw new Error("图片组不存在或已超过七天，无法载入完整图片组。");
      }
      if (!metadata.photo_only) throw new Error("暂不支持包含视频、文档或其他媒体的混合图片组。");
      const rows = db.all<
        { msg_id: number; file_id: string; file_unique_id: string; byte_size: number | null }
      >(SQL.AlbumFiles, message.chat.id, metadata.topic_id, group);
      if (!rows.length || rows.length > 10) throw new Error("图片组索引不完整或超出大小限制。");
      return rows.map((row) => ({
        fileId: row.file_id,
        uniqueId: row.file_unique_id,
        kind: "image",
        mimeType: "image/jpeg",
        byteSize: row.byte_size ?? undefined,
      }));
    },
    cleanup(unix = Math.floor(Date.now() / 1000)) {
      db.run(SQL.ExpireAlbums, unix);
      for (const [id, value] of activity) if (Date.now() - value.seen > 60_000) activity.delete(id);
    },
  };
}
