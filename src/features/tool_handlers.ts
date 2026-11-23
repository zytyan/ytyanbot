import { type Bot, type Context, InputFile } from "grammy";
import type { ConfigStore } from "../config.ts";
import type { MainDatabase } from "../db/database.ts";
import { SQL } from "../db/queries.ts";
import type { Interaction } from "../interaction.ts";
import { BoundedCache, KeyedMutex, Semaphore } from "../shared/concurrency.ts";
import type { TelegramPort } from "../shared/types.ts";
import { evaluateCalculation, formatCalculation, isCalculation } from "./calculator.ts";
import { fish, isFish, prpr, webpToPng } from "./images.ts";
import {
  exchange,
  formatExchangeUpdate,
  ocr,
  parseExchange,
  UnsupportedCurrencyError,
} from "./tools.ts";

export function toolHandlers(
  bot: Bot,
  db: MainDatabase,
  telegram: TelegramPort,
  configs: ConfigStore,
  current: (ctx: Context) => Interaction,
) {
  const ocrCache = new BoundedCache<string, string>(500, 3600_000);
  const locks = new KeyedMutex();
  const limiter = new Semaphore(1);
  let lastOcr = 0;
  const text = (ctx: Context) =>
    (ctx.message?.text ?? ctx.message?.caption ?? "")
      .replace(/^\/\S+\s*/, "")
      .replace(/(?:^|\s)@(?:\w+)(?=$|\s|[，。！？,.!?])/g, " ").trim();
  const bytes = async (id: string) => {
    const file = await telegram.getFile(id);
    if ((file.byteSize ?? 0) > 20 * 1024 * 1024) throw new Error("图片文件过大");
    const data = await telegram.downloadFile(file, AbortSignal.timeout(20_000));
    if (data.length > 20 * 1024 * 1024) throw new Error("图片文件过大");
    return data;
  };
  const math = async (ctx: Context, force = false) => {
    const expression = text(ctx);
    try {
      const { value, dice } = evaluateCalculation(expression);
      const result = `${expression} = ${formatCalculation(value)}`;
      const rolls = dice.map(({ sides, rolls }) => `${rolls.length}d${sides}：${rolls.join(" + ")}`)
        .join("；");
      const reply = rolls ? `骰子：${rolls}\n${result}` : result;
      if (rolls && reply.length > 4096) {
        await current(ctx).reply(`骰子：${rolls}`);
        await current(ctx).reply(result);
      } else {
        await current(ctx).reply(reply);
      }
      current(ctx).aiContext("calculator", reply);
    } catch {
      if (force) {
        await current(ctx).notice("计算失败，请检查表达式或计算范围。");
        current(ctx).aiContext("calculator", "计算失败。", "failure");
      }
    }
  };
  const currency = async (ctx: Context) => {
    const request = parseExchange(text(ctx));
    if (!request) return;
    try {
      const result = await exchange(request);
      const output = `${request.amount.toFixed(4)} ${request.from} = ${
        result.result.toFixed(4)
      } ${request.to}`;
      const reply = [output, formatExchangeUpdate(result.updated)].filter(Boolean).join("\n");
      await current(ctx).reply(reply);
      current(ctx).aiContext("exchange", reply);
    } catch (error) {
      if (error instanceof UnsupportedCurrencyError) return;
      await current(ctx).notice("汇率查询失败，请检查币种或稍后重试。");
      current(ctx).aiContext("exchange", "汇率查询失败。", "failure");
    }
  };
  bot.command("calc", (ctx) => math(ctx, true));
  bot.command("ocr", async (ctx) => {
    const target = ctx.message?.photo ? ctx.message : ctx.message?.reply_to_message;
    const photo = target?.photo?.at(-1);
    if (!photo) return await current(ctx).notice("没有图片");
    const result = await locks.use(photo.file_unique_id, async () => {
      const cached = ocrCache.get(photo.file_unique_id);
      if (cached !== undefined) return cached;
      const result = await limiter.use(async () => {
        const wait = Math.max(0, lastOcr + 200 - Date.now());
        if (wait) await new Promise((resolve) => setTimeout(resolve, wait));
        lastOcr = Date.now();
        return await ocr(await bytes(photo.file_id), configs.value.ocr);
      });
      ocrCache.set(photo.file_unique_id, result);
      return result;
    });
    // Telegram text messages have a 4096 character limit. Keep every OCR line.
    const chunks = [...(result || "未识别到文字")];
    for (let i = 0; i < chunks.length; i += 4000) {
      await current(ctx).reply(chunks.slice(i, i + 4000).join(""));
      current(ctx).aiContext("ocr", chunks.slice(i, i + 4000).join(""));
    }
  });
  bot.command("prpr", async (ctx) => {
    const user = ctx.message?.reply_to_message?.from ?? ctx.from;
    if (!user) return;
    const photos = await ctx.api.getUserProfilePhotos(user.id, { limit: 1 });
    const photo = photos.photos[0]?.at(-1);
    if (!photo) return await current(ctx).notice("该用户没有可用头像");
    await locks.use(`prpr:${photo.file_unique_id}`, async () => {
      const cached = db.get<{ prpr_file_id: string }>(SQL.GetPrprCache, photo.file_unique_id);
      const sticker = cached?.prpr_file_id ??
        new InputFile(await prpr(await bytes(photo.file_id)), "prpr.webp");
      const sent = await ctx.replyWithSticker(sticker, {
        reply_parameters: { message_id: ctx.message!.message_id },
      });
      if (sent.sticker) db.run(SQL.SetPrprCache, photo.file_unique_id, sent.sticker.file_id);
      current(ctx).aiContext(
        "prpr",
        `已生成 ${user.first_name} 的头像舔图。`,
        "success",
        sent.message_id,
      );
    });
  });
  bot.command("webp2png", async (ctx) => {
    const target = ctx.message?.sticker || ctx.message?.document
      ? ctx.message
      : ctx.message?.reply_to_message;
    const sticker = target?.sticker;
    const id = sticker && !sticker.is_animated && !sticker.is_video
      ? sticker.file_id
      : target?.document?.mime_type === "image/webp"
      ? target.document.file_id
      : undefined;
    if (!id) return await current(ctx).notice("请回复静态 WebP 表情或文件");
    const sent = await ctx.replyWithDocument(
      new InputFile(await webpToPng(await bytes(id)), "image.png"),
      {
        reply_parameters: { message_id: ctx.message!.message_id },
      },
    );
    current(ctx).aiContext("webp2png", "已将 WebP 转为 PNG 文件。", "success", sent.message_id);
  });
  return async (ctx: Context, autoCalculate: boolean, autoExchange: boolean): Promise<boolean> => {
    const source = text(ctx);
    if (ctx.message?.text?.startsWith("/")) return false;
    if (autoCalculate && isCalculation(source)) {
      await math(ctx);
      return true;
    }
    if (autoExchange && parseExchange(source)) {
      await currency(ctx);
      return true;
    }
    if (isFish(source)) {
      const file = new InputFile(await fish(source), "fish.jpg");
      const sent = source.length > 100
        ? await ctx.replyWithDocument(file, {
          reply_parameters: { message_id: ctx.message!.message_id },
        })
        : await ctx.replyWithPhoto(file, {
          reply_parameters: { message_id: ctx.message!.message_id },
        });
      current(ctx).aiContext("fish", `已拼接鱼图：${source}`, "success", sent.message_id);
      return true;
    }
    return false;
  };
}
