import type { MediaObject, MediaStore } from "../shared/types.ts";

// Telegram identifiers are opaque. Hex encoding is reversible and path-safe; no content hashing.
export function mediaPath(fileUniqueId: string): string {
  if (!fileUniqueId || new TextEncoder().encode(fileUniqueId).length > 120) {
    throw new Error("invalid Telegram file unique ID");
  }
  const key = Array.from(
    new TextEncoder().encode(fileUniqueId),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
  return `telegram/${key}`;
}

export class TelegramMediaStore implements MediaStore {
  constructor(readonly root: string) {}

  async put(bytes: Uint8Array, media: MediaObject): Promise<MediaObject> {
    const path = `${this.root}/${mediaPath(media.fileUniqueId)}`;
    if (bytes.byteLength !== media.byteSize) throw new Error("media size mismatch");
    await Deno.mkdir(`${this.root}/telegram`, { recursive: true });
    const temp = await Deno.makeTempFile({ dir: `${this.root}/telegram`, prefix: ".tmp-" });
    try {
      const file = await Deno.open(temp, { write: true, truncate: true });
      try {
        let offset = 0;
        while (offset < bytes.length) {
          const written = await file.write(bytes.subarray(offset));
          if (!written) throw new Error("media write made no progress");
          offset += written;
        }
        await file.sync();
      } finally {
        file.close();
      }
      await Deno.rename(temp, path);
    } catch (error) {
      try {
        await Deno.remove(temp);
      } catch { /* Best-effort cleanup preserves the original error. */ }
      throw error;
    }
    return media;
  }

  open(fileUniqueId: string): Promise<Uint8Array> {
    return Deno.readFile(`${this.root}/${mediaPath(fileUniqueId)}`);
  }

  async verify(fileUniqueId: string): Promise<boolean> {
    try {
      await this.open(fileUniqueId);
      return true;
    } catch (error) {
      if (error instanceof Deno.errors.NotFound) return false;
      throw error;
    }
  }

  // Offline only; catalogued objects are retained, regardless of message references.
  async collectGarbage(
    retained: ReadonlySet<string>,
    graceMs: number,
    dryRun = true,
    now = Date.now(),
  ): Promise<Array<{ fileUniqueId: string; byteSize: number }>> {
    if (!Number.isFinite(graceMs) || graceMs <= 0) {
      throw new Error("positive media GC grace required");
    }
    const keep = new Set(Array.from(retained, mediaPath));
    const removed = [];
    let entries: Deno.DirEntry[];
    try {
      entries = await Array.fromAsync(Deno.readDir(`${this.root}/telegram`));
    } catch (error) {
      if (error instanceof Deno.errors.NotFound) return [];
      throw error;
    }
    for (const entry of entries) {
      if (
        !entry.isFile || !/^(?:[0-9a-f]{2})+$/.test(entry.name) ||
        keep.has(`telegram/${entry.name}`)
      ) continue;
      const path = `${this.root}/telegram/${entry.name}`;
      const stat = await Deno.lstat(path);
      if (!stat.isFile || !stat.mtime || now - stat.mtime.getTime() < graceMs) continue;
      const fileUniqueId = new TextDecoder("utf-8", { fatal: true }).decode(
        Uint8Array.from(entry.name.match(/../g)!, (b) => parseInt(b, 16)),
      );
      if (mediaPath(fileUniqueId) !== `telegram/${entry.name}`) continue;
      removed.push({ fileUniqueId, byteSize: stat.size });
      if (!dryRun) await Deno.remove(path);
    }
    return removed;
  }
}
