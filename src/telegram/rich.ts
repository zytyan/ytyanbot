import type { RichBlock, RichBlockListItem, RichBlockTableCell, RichText } from "grammy/types";

/** Read received blocks, never the markdown/html fields used only when sending. */
export function richText(text: RichText, markdown = false): string {
  if (typeof text === "string") return text;
  if (Array.isArray(text)) return text.map((part) => richText(part, markdown)).join("");
  if (text.type === "custom_emoji") return text.alternative_text;
  if (text.type === "mathematical_expression") return text.expression;
  if (!("text" in text)) return "";
  const content = richText(text.text, markdown);
  if (!markdown) return content;
  switch (text.type) {
    case "url":
      return `[${content}](${text.url.replace(/[\\()]/g, "\\$&")})`;
    case "text_mention":
      return `[${content}](tg://user?id=${text.user.id})`;
    case "bold":
      return `**${content}**`;
    case "italic":
      return `_${content}_`;
    case "strikethrough":
      return `~~${content}~~`;
    case "spoiler":
      return `||${content}||`;
    case "code": {
      const delimiter = "`".repeat(
        Math.max(1, ...[...content.matchAll(/`+/g)].map((m) => m[0].length + 1)),
      );
      return `${delimiter} ${content} ${delimiter}`;
    }
    default:
      return content;
  }
}

export function richBlocksText(blocks: readonly RichBlock[], markdown = false): string {
  const text = (value: RichText) => richText(value, markdown);
  return blocks.map((block): string => {
    const parts: string[] = [];
    if ("summary" in block) parts.push(text(block.summary));
    if ("text" in block) {
      const content = text(block.text);
      if (markdown && block.type === "pre") {
        const delimiter = "`".repeat(
          Math.max(3, ...[...content.matchAll(/`+/g)].map((m) => m[0].length + 1)),
        );
        parts.push(
          `${delimiter}${block.language?.replace(/[^\w.+-]/g, "") ?? ""}\n${content}\n${delimiter}`,
        );
      } else if (markdown && block.type === "heading") {
        parts.push(`${"#".repeat(block.size)} ${content}`);
      } else parts.push(content);
    }
    if ("expression" in block) parts.push(block.expression);
    if ("blocks" in block) parts.push(richBlocksText(block.blocks, markdown));
    if (block.type === "list") {
      parts.push(
        block.items.map((item: RichBlockListItem) =>
          `${item.label} ${item.has_checkbox ? (item.is_checked ? "[x] " : "[ ] ") : ""}${
            richBlocksText(item.blocks, markdown)
          }`
        ).join("\n"),
      );
    }
    if (block.type === "table") {
      parts.push(
        block.cells.map((row: RichBlockTableCell[]) =>
          row.map((cell) => cell.text ? text(cell.text) : "").join(" | ")
        )
          .join("\n"),
      );
    }
    if ("location" in block) parts.push(`${block.location.latitude}, ${block.location.longitude}`);
    if ("caption" in block && block.caption) {
      if (block.type === "table") parts.push(text(block.caption));
      else {
        parts.push(text(block.caption.text));
        if (block.caption.credit) parts.push(text(block.caption.credit));
      }
    }
    if ("credit" in block && block.credit) parts.push(text(block.credit));
    if (block.type === "divider") parts.push("---");
    return parts.filter(Boolean).join("\n");
  }).filter(Boolean).join("\n\n");
}

/** Include nested media in lists, quotations, details, collages and slideshows. */
export function* walkRichBlocks(blocks: readonly RichBlock[]): Generator<RichBlock> {
  for (const block of blocks) {
    yield block;
    if ("blocks" in block) yield* walkRichBlocks(block.blocks);
    if (block.type === "list") {
      for (const item of block.items) yield* walkRichBlocks(item.blocks);
    }
  }
}
