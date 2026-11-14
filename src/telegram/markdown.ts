import type { Message, MessageEntity } from "grammy/types";

import { richBlocksText } from "./rich.ts";

interface Span {
  entity: MessageEntity;
  start: number;
  end: number;
  children: Span[];
}

const formats = new Set([
  "bold",
  "italic",
  "underline",
  "strikethrough",
  "spoiler",
  "code",
  "pre",
  "blockquote",
  "expandable_blockquote",
  "text_link",
  "text_mention",
]);
const escape = (text: string) => text.replace(/[\\*_~\[\]|]/g, "\\$&");
const destination = (url: string) =>
  url.replace(/[\\()]/g, "\\$&").replace(/[\r\n]/g, (character) => encodeURIComponent(character));
const fence = (text: string, minimum: number) =>
  "`".repeat(Math.max(minimum, ...[...text.matchAll(/`+/g)].map((run) => run[0].length + 1)));

/** Telegram entity offsets are UTF-16, matching JavaScript's slice indexes. */
export function messageMarkdown(message: Message): string {
  if (message.rich_message) return richBlocksText(message.rich_message.blocks, true);
  const text = message.text ?? message.caption ?? "";
  const entities = message.text !== undefined ? message.entities : message.caption_entities;
  const roots: Span[] = [], stack: Span[] = [];
  for (
    const entity of [...(entities ?? [])].filter((entity) => formats.has(entity.type))
      .sort((a, b) => a.offset - b.offset || b.length - a.length)
  ) {
    const start = entity.offset, end = start + entity.length;
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end > text.length) {
      continue;
    }
    if (end <= start) continue;
    while (stack.length && start >= stack.at(-1)!.end) stack.pop();
    const parent = stack.at(-1);
    if (parent && end > parent.end) continue; // Ignore invalid crossing entities.
    const span = { entity, start, end, children: [] };
    (parent?.children ?? roots).push(span);
    stack.push(span);
  }
  if (!roots.length) return text;
  const render = (start: number, end: number, spans: Span[]): string => {
    let result = "", cursor = start;
    for (const span of spans) {
      result += escape(text.slice(cursor, span.start));
      const entity = span.entity, raw = text.slice(span.start, span.end);
      if (entity.type === "pre") {
        const delimiter = fence(raw, 3), language = entity.language?.replace(/[^\w.+-]/g, "") ?? "";
        result += `\n${delimiter}${language}\n${raw}${
          raw.endsWith("\n") ? "" : "\n"
        }${delimiter}\n`;
      } else if (entity.type === "code") {
        const delimiter = fence(raw, 1);
        const padding = /^[` ]|[` ]$/.test(raw) ? " " : "";
        result += `${delimiter}${padding}${raw}${padding}${delimiter}`;
      } else {
        const content = render(span.start, span.end, span.children);
        switch (entity.type) {
          case "bold":
            result += `**${content}**`;
            break;
          case "italic":
            result += `_${content}_`;
            break;
          case "underline":
            result += `<u>${content}</u>`;
            break;
          case "strikethrough":
            result += `~~${content}~~`;
            break;
          case "spoiler":
            result += `||${content}||`;
            break;
          case "blockquote":
          case "expandable_blockquote":
            result += `\n> ${content.replaceAll("\n", "\n> ")}\n`;
            break;
          case "text_link":
            result += `[${content}](${destination(entity.url)})`;
            break;
          case "text_mention":
            result += `[${content}](tg://user?id=${entity.user.id})`;
            break;
          default:
            result += content;
        }
      }
      cursor = span.end;
    }
    return result + escape(text.slice(cursor, end));
  };
  return render(0, text.length, roots);
}
