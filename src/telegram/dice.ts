import type { Dice } from "grammy/types";

const slotSymbols = ["BAR", "葡萄", "柠檬", "7"];

/** Render Telegram's server-side Dice value for readers who cannot see the animation. */
export function diceText({ emoji, value }: Dice): string {
  switch (emoji) {
    case "🎲":
      return `🎲 掷骰子，结果：${value} 点`;
    case "⚽":
      return `⚽ 足球射门，结果：${value >= 4 ? "进球" : "未进球"}（Telegram 数值 ${value}）`;
    case "🏀":
      return `🏀 篮球投篮，结果：${value >= 4 ? "投进" : "未投进"}（Telegram 数值 ${value}）`;
    case "🎯": {
      const outcome = value === 6 ? "命中靶心" : value === 1 ? "脱靶" : "命中靶面";
      return `🎯 投飞镖，结果：${outcome}（Telegram 数值 ${value}）`;
    }
    case "🎳":
      return value === 6
        ? "🎳 保龄球，结果：全中（Telegram 数值 6）"
        : `🎳 保龄球，结果：Telegram 数值 ${value}`;
    case "🎰": {
      if (!Number.isInteger(value) || value < 1 || value > 64) break;
      // Telegram packs the left, center and right reels into successive two-bit values.
      const reels = [0, 2, 4].map((shift) => slotSymbols[((value - 1) >> shift) & 3]).join(" · ");
      return `🎰 转动老虎机，结果：${reels}${value === 64 ? "（头奖）" : ""}`;
    }
  }
  return `${emoji} Telegram 随机动画，结果数值：${value}`;
}
