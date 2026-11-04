import { run, type RunnerHandle } from "@grammyjs/runner";
import { Bot, Context } from "grammy";
import type { InlineKeyboardMarkup, InlineQueryResult, Message, PhotoSize } from "grammy/types";
import { AiRepository } from "./ai/repository.ts";
import {
  AiService,
  DEFAULT_SYSTEM_PROMPT,
  type IncomingAiMessage,
  type IncomingMedia,
} from "./ai/service.ts";
import { createAiGenerator } from "./ai/providers.ts";
import { type AiProviderName, MODEL_OPTIONS, parseModelCallback } from "./ai/types.ts";
import { TelegramMediaStore } from "./ai/media_store.ts";
import type { ConfigStore } from "./config.ts";
import type { MainDatabase } from "./db/database.ts";
import {
  BilibiliLinkError,
  convertBilibiliLinks,
  hasBilibiliVideo,
  parseBilibiliInlineId,
} from "./download/bilibili.ts";
import { DownloadService, parseDownloadKey } from "./download/service.ts";
import { type NsfwPhoto, NsfwService } from "./nsfw/service.ts";
import { DenoProcessRunner } from "./shared/process.ts";
import { toolHandlers } from "./features/tool_handlers.ts";
import { type ConfigField, StatsService } from "./stats/service.ts";
import { GrammyTelegramGateway } from "./telegram/gateway.ts";
import { topicId } from "./telegram/topic.ts";
import { messageMarkdown } from "./telegram/markdown.ts";
import { registerRoll } from "./features/roll.ts";
import { type Interaction, interaction, isAiTrigger, type TraditionalFact } from "./interaction.ts";
import { albumIndex } from "./ai/albums.ts";
import { SQL } from "./db/queries.ts";
import { installLogging, startInnerHttp } from "./inner_http.ts";
import { logger } from "./logging.ts";
import { formatUsage } from "./ai/usage.ts";
import { parseSuspension } from "./ai/suspend.ts";
import { isCalculation } from "./features/calculator.ts";
import { isFish } from "./features/images.ts";
import { parseExchange } from "./features/tools.ts";
import {
  backgroundTiming,
  forgetUpdate,
  latencyStage,
  startLatencyMonitor,
  traceUpdate,
  updatesReceived,
} from "./shared/latency.ts";
import {
  editMenuMarkup,
  editMenuText,
  incomingPrivateCommand,
  PRIVATE_COMMANDS,
  privateReplyOptions,
  recordCallbackReceived,
  registerCommandMenus,
  replyMenuPrivately,
  replyPrivately,
} from "./telegram/ephemeral.ts";
import type { DownloadNotice } from "./download/notice.ts";

function downloadNotice(ctx: Context): DownloadNotice {
  return {
    async send() {
      const message = await replyPrivately(ctx, "正在下载，请稍等");
      return message
        ? { chatId: message.chat.id, messageId: message.message_id, raw: message }
        : undefined;
    },
    async remove(sent) {
      const message = sent.raw as Message;
      if (message.ephemeral_message_id) {
        await ctx.api.deleteEphemeralMessage(
          message.chat.id,
          ctx.from!.id,
          message.ephemeral_message_id,
        );
      } else if (message.message_id) {
        await ctx.api.deleteMessage(message.chat.id, message.message_id);
      }
    },
  };
}

const SYSTEM_PROMPT_HELP =
  `没有找到任何System prompt，请使用 /sysprompt 提示词或使用该命令回复其他消息设置提示词。
您需要使用 /get_sysprompt 获取当前系统提示词， /reset_sysprompt 恢复默认系统提示词。

你可以通过 %VAR% 使用稳定变量。为提高缓存命中率，逐轮变化的信息只出现在最新用户消息的「[ 显示名 时间 ]」消息头中。
CHAT_NAME: 当前聊天的名称
CHAT_ID: 当前聊天 ID
BOT_NAME: Bot的名字
BOT_USERNAME: Bot的username
CHAT_TYPE: 聊天类型(group, private)`;

function messageText(message?: Message): string {
  return message?.text ?? message?.caption ?? "";
}

function commandArgument(message?: Message): string {
  return messageText(message).replace(/^\/\S+\s*/, "").trim();
}

function escapeHtml(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

function largestPhoto(message?: Message): PhotoSize | undefined {
  return message?.photo?.at(-1);
}

function photoInput(photo: PhotoSize): NsfwPhoto {
  return {
    fileId: photo.file_id,
    fileUniqueId: photo.file_unique_id,
    width: photo.width,
    height: photo.height,
  };
}

function incomingMedia(message: Message): IncomingMedia[] {
  const photo = largestPhoto(message);
  if (photo) {
    return [{
      fileId: photo.file_id,
      uniqueId: photo.file_unique_id,
      kind: "image",
      mimeType: "image/jpeg",
      byteSize: photo.file_size,
    }];
  }
  if (message.sticker) {
    if (message.sticker.is_animated) return [];
    return [{
      fileId: message.sticker.file_id,
      uniqueId: message.sticker.file_unique_id,
      kind: message.sticker.is_video ? "video" : "sticker",
      mimeType: message.sticker.is_video ? "video/webm" : "image/webp",
      byteSize: message.sticker.file_size,
    }];
  }
  if (message.video) {
    return [{
      fileId: message.video.file_id,
      uniqueId: message.video.file_unique_id,
      kind: "video",
      mimeType: message.video.mime_type ?? "video/mp4",
      byteSize: message.video.file_size,
      duration: message.video.duration,
    }];
  }
  if (message.animation) {
    return [{
      fileId: message.animation.file_id,
      uniqueId: message.animation.file_unique_id,
      kind: "animation",
      mimeType: message.animation.mime_type ?? "video/mp4",
      byteSize: message.animation.file_size,
      duration: message.animation.duration,
    }];
  }
  return [];
}

function toIncoming(message: Message, withReply = true): IncomingAiMessage {
  const from = message.from;
  return {
    chatId: message.chat.id,
    chatName: "title" in message.chat
      ? message.chat.title ?? ""
      : [message.chat.first_name, message.chat.last_name].filter(Boolean).join(" "),
    chatType: message.chat.type,
    topicId: message.is_topic_message ? topicId(message) : undefined,
    messageId: message.message_id,
    sentAt: message.date,
    userId: from?.id ?? message.sender_chat?.id ?? 0,
    isBot: from?.is_bot ?? false,
    username: from
      ? [from.first_name, from.last_name].filter(Boolean).join(" ")
      : message.sender_chat?.title ?? "未知用户",
    atableUsername: from?.username,
    text: messageMarkdown(message) || message.sticker?.emoji ||
      (message.sticker ? "[贴纸]" : undefined),
    reply: withReply && message.reply_to_message
      ? toIncoming(message.reply_to_message, false)
      : undefined,
    media: incomingMedia(message),
  };
}

function replyOptions(message?: Message): Record<string, unknown> {
  return message ? { reply_parameters: { message_id: message.message_id } } : {};
}

function configKeyboard(stats: StatsService, chatId: number): InlineKeyboardMarkup {
  const cfg = stats.config(chatId);
  const options: [ConfigField, string, boolean][] = [
    ["auto_cvt_bili", "自动转换Bilibili链接", cfg.autoCvtBili],
    ["auto_calculate", "自动计算", cfg.autoCalculate],
    ["auto_exchange", "自动换算汇率", cfg.autoExchange],
    ["auto_check_adult", "自动图片审核", cfg.autoCheckAdult],
    ["resp_nsfw_msg", "响应来张色图", cfg.respNsfwMsg],
  ];
  return {
    inline_keyboard: options.map(([field, label, enabled]) => [{
      text: `${enabled ? "✅" : "❌"} ${label}`,
      callback_data: `gcfg:${field}:${enabled ? "T" : "F"}`,
    }]),
  };
}

function modelKeyboard(
  current: string,
  provider: AiProviderName,
  sessionId = 0,
): InlineKeyboardMarkup {
  return {
    inline_keyboard: MODEL_OPTIONS.map((option, index) => ({ option, index })).filter((
      { option, index },
    ) =>
      MODEL_OPTIONS.findIndex((candidate) =>
        candidate.provider === option.provider && candidate.model === option.model
      ) === index
    ).map(({ option, index }) => [{
      text: `${
        option.model === current && option.provider === provider ? "✅ " : ""
      }${option.label}`,
      callback_data: `aimodel2:${sessionId}:${index}`,
    }]),
  };
}

function userLabel(message: Message): string {
  const from = message.from;
  return from ? [from.first_name, from.last_name].filter(Boolean).join(" ") : "未知用户";
}

async function canAdminister(ctx: Context, god: number): Promise<boolean> {
  if (!ctx.from || !ctx.chat) return false;
  if (ctx.from.id === god || ctx.chat.type === "private") return true;
  const member = await ctx.api.getChatMember(ctx.chat.id, ctx.from.id);
  return member.status === "administrator" || member.status === "creator";
}

export interface Application {
  readonly bot: Bot;
  serve(signal: AbortSignal): Promise<void>;
  flush(): void;
}

export async function createApplication(
  db: MainDatabase,
  configs: ConfigStore,
  configureBot?: (bot: Bot) => void,
): Promise<Application> {
  const initial = configs.value;
  if (!initial.botToken) throw new Error("bot-token is empty");
  const bot = new Bot(initial.botToken, {
    client: { apiRoot: initial.telegramApiUrl, timeoutSeconds: 130 },
  });
  configureBot?.(bot);
  bot.api.config.use(async (previous, method, payload, signal) => {
    if (method !== "getUpdates") {
      return await latencyStage(
        `telegram.${method}`,
        () => previous(method, payload, signal),
        (result) => !result.ok,
      );
    }
    const result = await previous(method, payload, signal);
    if (result.ok && Array.isArray(result.result)) {
      updatesReceived((result.result as { update_id: number }[]).map((update) => update.update_id));
    }
    return result;
  });
  await bot.init();
  const identity = bot.botInfo;
  const telegram = new GrammyTelegramGateway(bot, initial.botToken, initial.telegramApiUrl);
  const repository = new AiRepository(db);
  const stats = new StatsService(db);
  const ai = new AiService(
    repository,
    createAiGenerator(() => configs.value),
    telegram,
    new TelegramMediaStore(initial.aiMediaPath),
    () => configs.value,
    { id: identity.id, firstName: identity.first_name, username: identity.username },
  );
  const downloads = new DownloadService(db, telegram, new DenoProcessRunner());
  const nsfw = new NsfwService(
    db,
    telegram,
    () => configs.value,
    (chatId, kind) => stats.increment(chatId, kind),
  );
  const albums = albumIndex(db);
  const now = () => Math.floor(Date.now() / 1000);
  const interactions = new WeakMap<Context, Interaction>();
  const currentInteraction = (ctx: Context) => interactions.get(ctx)!;
  const shutdown = new AbortController();
  const earlyActivity = new WeakMap<Context, () => void>();

  bot.use(async (ctx, next) => {
    const message = ctx.message;
    const text = messageText(message);
    const aiTrigger = message && configs.value.aiChats.includes(message.chat.id) &&
      isAiTrigger(text, identity.username, message.reply_to_message?.from?.id === identity.id);
    // AI feedback starts before automatic-feature classification or its configuration lookup.
    if (aiTrigger) {
      return await traceUpdate(ctx.update.update_id, "ai", message.date, next);
    }
    const possibleAutomatic = message &&
      (message.photo || /b23\.tv|bilibili\.com|bili2233\.cn/i.test(text) || isCalculation(text) ||
        parseExchange(text) || isFish(text));
    const cfg = possibleAutomatic ? stats.config(message.chat.id) : undefined;
    const automatic = message && cfg &&
      (message.photo && cfg.autoCheckAdult ||
        /b23\.tv|bilibili\.com|bili2233\.cn/i.test(text) && cfg.autoCvtBili ||
        isCalculation(text) && cfg.autoCalculate || parseExchange(text) && cfg.autoExchange ||
        isFish(text));
    if (
      !ctx.callbackQuery && !ctx.inlineQuery && !text.startsWith("/") && !aiTrigger && !automatic
    ) {
      forgetUpdate(ctx.update.update_id);
      return await next();
    }
    const kind = ctx.callbackQuery
      ? "callback"
      : ctx.inlineQuery
      ? "inline"
      : aiTrigger
      ? "ai"
      : text.startsWith("/")
      ? "command"
      : "automatic";
    return await traceUpdate(ctx.update.update_id, kind, message?.date, next);
  });

  bot.use(async (ctx, next) => {
    const message = ctx.message;
    if (
      !message || message.ephemeral_message_id ||
      !configs.value.aiChats.includes(message.chat.id) ||
      !isAiTrigger(
        messageText(message),
        identity.username,
        message.reply_to_message?.from?.id === identity.id,
      ) ||
      repository.runForRequest(message.chat.id, message.message_id)?.status === "delivered" ||
      ai.isRequestSuspended(toIncoming(message))
    ) {
      return await next();
    }
    const stop = ai.startActivity({ ...toIncoming(message), signal: shutdown.signal });
    earlyActivity.set(ctx, stop);
    try {
      await next();
    } finally {
      stop();
    }
  });

  // Outer middleware resumes after commands and traditional message handlers finish.
  bot.use(async (ctx, next) => {
    recordCallbackReceived(ctx);
    if (!ctx.message) return await next();
    const message = ctx.message;
    if (message.ephemeral_message_id) {
      if (!await privateReplyOptions(ctx)) return;
      if (!PRIVATE_COMMANDS.has(incomingPrivateCommand(message))) {
        await replyPrivately(
          ctx,
          "临时输入仅支持会话查询、帮助和设置命令；AI 对话及工具结果请使用普通消息。",
        );
        return;
      }
      interactions.set(ctx, interaction(db, ctx));
      // IDs are temporary and reusable: do not query/persist facts, sessions, albums or statistics.
      await next();
      return;
    }
    if (configs.value.aiChats.includes(message.chat.id)) albums.capture(message);
    const topic = topicId(message);
    const current = interaction(db, ctx);
    interactions.set(ctx, current);
    const previous = db.all<TraditionalFact>(
      SQL.RequestFacts,
      message.chat.id,
      topic,
      message.message_id,
    );
    if (previous.length) current.facts.push(...previous);
    else {
      try {
        await latencyStage("traditional", next);
      } catch (error) {
        console.warn("traditional handler failed", error);
        try {
          await current.notice("操作失败，请稍后重新发起请求。");
        } catch { /* No automatic side-effect retry. */ }
        current.aiContext("traditional", "传统功能执行或交付失败。", "failure");
      }
    }
    const replied = message.reply_to_message;
    const botReply = replied?.from?.id === identity.id;
    if (!isAiTrigger(messageText(message), identity.username, botReply)) return;
    if (!configs.value.aiChats.includes(message.chat.id)) return;
    const replyFacts = replied
      ? db.all<TraditionalFact>(
        SQL.ReplyFacts,
        message.chat.id,
        topicId(replied),
        replied.message_id,
      )
      : [];
    const incoming = toIncoming(message);
    if (ai.isRequestSuspended(incoming)) return;
    if (!repository.runForRequest(message.chat.id, message.message_id)) {
      if (!albums.claim(message)) return;
      try {
        const media = await latencyStage(
          "album.current",
          () => albums.media(message, shutdown.signal),
        );
        if (media) Object.assign(incoming, { media, completeAlbum: true });
        if (replied && incoming.reply) {
          const replyMedia = await latencyStage(
            "album.reply",
            () => albums.media(replied, shutdown.signal),
          );
          if (replyMedia) Object.assign(incoming.reply, { media: replyMedia, completeAlbum: true });
        }
      } catch (error) {
        await current.reply(error instanceof Error ? error.message : "无法读取完整图片组。");
        return;
      }
    }
    await ai.handle({
      ...incoming,
      facts: [...replyFacts, ...current.facts],
      signal: shutdown.signal,
    }, earlyActivity.get(ctx));
  });

  bot.use(async (ctx, next) => {
    const message = ctx.message;
    if (message?.from && !message.ephemeral_message_id) {
      stats.record({
        chatId: message.chat.id,
        messageId: message.message_id,
        sentAt: message.date,
        user: {
          id: message.from.id,
          firstName: message.from.first_name,
          lastName: message.from.last_name,
          username: message.from.username,
        },
        text: message.text ?? "",
        photo: Boolean(message.photo),
        video: Boolean(message.video),
        sticker: Boolean(message.sticker),
        forwarded: Boolean(message.forward_origin),
      });
    }
    await next();
  });

  // Register specific commands/callbacks before the general message/callback handlers.
  registerRoll(
    bot,
    (ctx, text, status, replyId) =>
      currentInteraction(ctx).aiContext("roll", text, status, replyId),
  );

  const automaticTools = toolHandlers(bot, db, telegram, configs, currentInteraction);

  bot.command("suspend", async (ctx) => {
    const options = await privateReplyOptions(ctx);
    // This command must never fall back to a public group reply.
    if (
      !options || (["group", "supergroup"].includes(ctx.chat.type) &&
        !options.ephemeral_message_parameters)
    ) return;
    const reply = (text: string) => ctx.api.sendMessage(ctx.chat.id, text, options);
    if (ctx.from?.id !== configs.value.god) return await reply("此命令仅限 Bot 所有者使用。");
    if (ctx.msg.ephemeral_message_id && Date.now() - ctx.msg.date * 1000 >= 15_000) return;
    const value = commandArgument(ctx.message);
    const duration = parseSuspension(value);
    if (
      duration === undefined ||
      (typeof duration === "number" && !Number.isSafeInteger(now() + duration))
    ) {
      return await reply(
        "用法：回复目标会话消息发送 /suspend [10s|1m|2h|1d|off]；留空永久暂停，off 恢复。",
      );
    }
    const replied = ctx.msg.reply_to_message;
    const session = replied && ai.sessionForMessage(ctx.chat.id, replied.message_id);
    if (!session) return await reply("请回复目标 AI 会话中的消息使用此命令。");
    ai.setSessionSuspension(
      session.id,
      typeof duration === "number" ? now() + duration : duration,
    );
    await reply(
      duration === "off"
        ? `会话 #${session.id} 已恢复 AI 回复。`
        : duration === null
        ? `会话 #${session.id} 已永久暂停 AI 回复。`
        : `会话 #${session.id} 已暂停 AI 回复 ${value}，到期自动恢复。`,
    );
  });

  bot.command("ai_reaction", async (ctx) => {
    const value = commandArgument(ctx.message).toLowerCase();
    if (!ctx.from || !["on", "off"].includes(value)) {
      return await replyPrivately(ctx, "用法：/ai_reaction on|off", replyOptions(ctx.message));
    }
    repository.setReactions(ctx.from.id, value === "on", now());
    await replyPrivately(
      ctx,
      `AI 表情回应已${value === "on" ? "开启" : "关闭"}`,
      replyOptions(ctx.message),
    );
  });

  bot.command("sysprompt", async (ctx) => {
    const message = ctx.msg!;
    const prompt = commandArgument(message) ||
      (message.reply_to_message ? messageText(message.reply_to_message).trim() : "");
    if (!prompt) return await replyPrivately(ctx, SYSTEM_PROMPT_HELP, replyOptions(message));
    repository.setSystemPrompt(message.chat.id, topicId(message), prompt, now());
    await replyPrivately(
      ctx,
      "已设置当前话题的系统提示词（影响该话题所有成员）。",
      replyOptions(message),
    );
  });
  bot.command("reset_sysprompt", async (ctx) => {
    repository.resetSystemPrompt(ctx.chat.id, topicId(ctx.message));
    await replyPrivately(
      ctx,
      "已恢复当前话题的默认提示词（影响该话题所有成员）。",
      replyOptions(ctx.message),
    );
  });
  bot.command("get_sysprompt", async (ctx) => {
    const prompt = repository.getSystemPrompt(ctx.chat.id, topicId(ctx.message)) ??
      DEFAULT_SYSTEM_PROMPT;
    await replyPrivately(ctx, prompt, replyOptions(ctx.message));
  });
  bot.command("new_session", async (ctx) => {
    ai.resetCurrent(ctx.chat.id, topicId(ctx.message));
    await replyPrivately(
      ctx,
      "已重置当前话题的默认会话（影响该话题所有成员）；下一条 AI 消息开始新会话，不携带历史记录。",
      replyOptions(ctx.message),
    );
  });
  bot.command("session_id", async (ctx) => {
    const replied = ctx.msg!.reply_to_message;
    const session = replied
      ? ai.sessionForMessage(ctx.chat.id, replied.message_id)?.id
      : ai.currentSessionId(ctx.chat.id, topicId(ctx.msg));
    const text = session
      ? `Session ID: ${session}`
      : replied
      ? "您回复的消息不在会话中。"
      : "当前没有session，您可以回复特定消息来获取该消息的 session ID";
    await replyPrivately(ctx, text, replyOptions(ctx.message));
  });
  bot.command("session_help", async (ctx) => {
    await replyPrivately(
      ctx,
      "会话相关帮助：\n/new_session 停止当前会话，创建新会话\n/session_id 获取当前会话ID，若回复特定消息，则获取该消息的会话ID",
      replyOptions(ctx.message),
    );
  });

  const showModels = async (ctx: Context) => {
    if (!ctx.message || !ctx.chat) return;
    if (!await canAdminister(ctx, configs.value.god)) {
      await replyPrivately(ctx, "只有群主或管理员可以切换模型", replyOptions(ctx.message));
      return;
    }
    let sessionId = 0;
    let current = repository.getSelection(ctx.chat.id).model;
    let provider = repository.getSelection(ctx.chat.id).provider;
    if (ctx.message.reply_to_message) {
      const session = ai.sessionForMessage(ctx.chat.id, ctx.message.reply_to_message.message_id);
      if (!session) {
        await replyPrivately(
          ctx,
          "被回复的消息不属于可切换的 AI 会话。",
          replyOptions(ctx.message),
        );
        return;
      }
      sessionId = session.id;
      current = session.model;
      provider = session.provider;
    }
    const prompt = sessionId
      ? `请选择历史会话 #${sessionId} 使用的 AI 模型：`
      : "请选择当前聊天使用的 AI 模型：";
    await replyMenuPrivately(ctx, prompt, modelKeyboard(current, provider, sessionId));
  };
  bot.command("change_model", showModels);
  bot.command("model", showModels);
  bot.command("show_usage", async (ctx) => {
    if (!await canAdminister(ctx, configs.value.god)) {
      await replyPrivately(ctx, "只有群主或管理员可以修改用量显示设置", replyOptions(ctx.message));
      return;
    }
    const enabled = repository.toggleUsage(ctx.chat.id, now());
    await replyPrivately(
      ctx,
      `已${enabled ? "开启" : "关闭"} 当前群的 AI Token 用量按钮（影响全群）。`,
      replyOptions(ctx.message),
    );
  });

  const downloadCommand = (audioOnly: boolean) => async (ctx: Context) => {
    if (!ctx.message || !ctx.from) return;
    const source = commandArgument(ctx.message) ||
      (ctx.message.reply_to_message ? messageText(ctx.message.reply_to_message) : "");
    try {
      const key = await parseDownloadKey(source, audioOnly);
      const sent = await downloads.deliver({
        key,
        chatId: ctx.chat!.id,
        messageId: ctx.message.message_id,
        userId: ctx.from.id,
        userName: userLabel(ctx.message),
        signal: shutdown.signal,
        notice: downloadNotice(ctx),
      });
      currentInteraction(ctx).aiContext(
        "download",
        `${audioOnly ? "音频" : "视频"}下载成功。来源：${key.url}`,
        "success",
        sent.messageId,
      );
      stats.increment(ctx.chat!.id, audioOnly ? "download_audio" : "download_video");
    } catch (error) {
      logger("download").warn("download failed", error);
      await currentInteraction(ctx).notice(
        error instanceof BilibiliLinkError
          ? error.message
          : "下载失败，请检查链接或稍后重新发起请求。",
      );
      currentInteraction(ctx).aiContext("download", "下载失败。", "failure");
    }
  };
  bot.command("downloadvideo", downloadCommand(false));
  bot.command("downloadaudio", downloadCommand(true));

  bot.command("score", async (ctx) => {
    const target = largestPhoto(ctx.msg) ? ctx.msg : ctx.msg!.reply_to_message;
    const photo = target ? largestPhoto(target) : undefined;
    if (!target || !photo) return await replyPrivately(ctx, "没有图片");
    try {
      const result = await nsfw.moderate(photoInput(photo));
      const severity = result.severity;
      const saved = nsfw.collect(photoInput(photo), result);
      const sent = await telegram.sendMessage(
        ctx.chat.id,
        `bot评分: ${severity}/6\n已入库: ${saved ? "是" : "否"}\n用户评分: ${
          saved?.userRate ?? severity
        }/6`,
        {
          reply_parameters: { message_id: target.message_id },
          ...(saved ? { reply_markup: nsfw.ratingKeyboard(photo.file_unique_id, "score") } : {}),
        },
      );
      currentInteraction(ctx).aiContext(
        "score",
        `图片审核评分：${severity}/6，图库用户评分：${saved?.userRate ?? severity}/6。`,
        "success",
        sent.messageId,
      );
    } catch {
      await currentInteraction(ctx).notice("识别失败");
      currentInteraction(ctx).aiContext("score", "图片审核失败。", "failure");
    }
  });
  bot.command(
    "getrank",
    async (ctx) => {
      const output = stats.rank(ctx.chat.id);
      await currentInteraction(ctx).reply(output);
      currentInteraction(ctx).aiContext("rank", `今日发言排行：\n${output}`);
    },
  );
  bot.command("diag_sendstat", async (ctx) => {
    stats.flush();
    await currentInteraction(ctx).reply(stats.dailyMessage(ctx.chat.id, now() - 86_400), {
      parse_mode: "HTML",
    });
    currentInteraction(ctx).aiContext("statistics", "已发送昨日群统计日报。");
  });
  bot.command("chat_config", async (ctx) => {
    await replyMenuPrivately(
      ctx,
      "当前群组设置（修改影响全群）：",
      configKeyboard(stats, ctx.chat.id),
    );
  });

  bot.on("inline_query", async (ctx) => {
    const query = ctx.inlineQuery.query;
    if (!query) {
      await ctx.answerInlineQuery([{
        type: "article",
        id: "empty",
        title: "这个家伙什么也没有说",
        input_message_content: { message_text: "这个家伙什么也没有说" },
      }], { cache_time: 0 });
      return;
    }
    const rawResult = (
      title = "没有检测到可转换的Bilibili链接或出现错误，点击可原样发送",
    ): InlineQueryResult[] => [{
      type: "article",
      id: "raw",
      title,
      input_message_content: { message_text: query },
    }];
    let results = rawResult();
    if (/b23\.tv|bilibili\.com|bili2233\.cn/i.test(query)) {
      try {
        const links = await latencyStage("inline.bilibili", () => convertBilibiliLinks(query));
        if (links.hasAv || links.hasBv) {
          const uid = await latencyStage(
            "inline.context",
            () => Promise.resolve(downloads.createInlineContext(now())),
          );
          const markup = hasBilibiliVideo(links.bvText)
            ? {
              inline_keyboard: [[{
                text: "下载视频",
                callback_data: `il:bili:${uid.toString(16)}`,
              }]],
            }
            : undefined;
          const result = (id: string, title: string, text: string): InlineQueryResult => ({
            type: "article",
            id,
            title,
            input_message_content: { message_text: text },
            reply_markup: markup,
          });
          results = [result(`bv-${uid}`, "转换后的链接", links.bvText)];
          if (links.hasAv) {
            results.push(result(`av-${uid}`, "转换后的链接（使用AV号）", links.avText));
          }
        }
      } catch (error) {
        const code = (error as { code?: unknown } | null)?.code;
        logger("download").warn("bilibili inline conversion failed", {
          error_type: error instanceof Error ? error.name : "unknown",
          code: typeof code === "string" && /^[A-Z_]{1,64}$/.test(code) ? code : undefined,
        });
        if (error instanceof BilibiliLinkError) results = rawResult(error.message);
      }
    }
    // Answer exactly once: Telegram API failures must not masquerade as conversion failures.
    await ctx.answerInlineQuery(results, { cache_time: 0, is_personal: true });
  });

  bot.on("message", async (ctx) => {
    const message = ctx.message;
    const text = messageText(message);
    const config = stats.config(message.chat.id);
    if (await automaticTools(ctx, config.autoCalculate, config.autoExchange)) return;

    if (message.via_bot?.id === identity.id && message.reply_markup) {
      for (const row of message.reply_markup.inline_keyboard) {
        for (const button of row) {
          if ("callback_data" in button && button.callback_data?.startsWith("il:bili:")) {
            const uid = parseBilibiliInlineId(button.callback_data);
            if (uid !== undefined) {
              downloads.updateInlineContext(uid, text, message.chat.id, message.message_id);
            }
          }
        }
      }
    }
    if (
      config.autoCvtBili && !message.via_bot && !text.startsWith("/") &&
      /b23\.tv|bilibili\.com|bili2233\.cn/i.test(text)
    ) {
      try {
        const converted = await convertBilibiliLinks(text);
        if (converted.needClean) {
          const sent = await telegram.sendMessage(
            message.chat.id,
            `来自<a href="tg://user?id=${ctx.from?.id ?? 0}">${
              escapeHtml(userLabel(message))
            }</a>的消息，其中的链接已自动转换\n\n${escapeHtml(converted.bvText)}`,
            {
              parse_mode: "HTML",
              reply_markup: hasBilibiliVideo(converted.bvText)
                ? {
                  inline_keyboard: [[{ text: "下载视频", callback_data: "download:bilibili" }]],
                }
                : undefined,
            },
          );
          currentInteraction(ctx).aiContext(
            "bilibili",
            `已转换 Bilibili 链接：${converted.bvText.slice(0, 1024)}`,
            "success",
            sent.messageId,
          );
          try {
            await telegram.deleteMessage(message.chat.id, message.message_id);
          } catch (error) {
            logger("download").warn("bilibili source cleanup failed", error);
          }
          return;
        }
      } catch (error) {
        logger("download").warn("bilibili conversion failed", error);
        currentInteraction(ctx).aiContext("bilibili", "B站链接转换失败。", "failure");
        if (error instanceof BilibiliLinkError) await currentInteraction(ctx).notice(error.message);
      }
    }
    const photo = largestPhoto(message);
    if (photo && config.autoCheckAdult) {
      try {
        await nsfw.detectAndReply(
          {
            chatId: message.chat.id,
            messageId: message.message_id,
            mediaGroupId: message.media_group_id,
            hasMediaSpoiler: message.has_media_spoiler ?? false,
            photo: photoInput(photo),
          },
          (sent, result) =>
            currentInteraction(ctx).aiContext(
              "moderation",
              `图片审核评分 ${result.severity}/6，已入图库。`,
              "success",
              sent.messageId,
            ),
        );
      } catch (error) {
        logger("moderation").warn("NSFW detection failed", error);
      }
    }
    if (
      config.respNsfwMsg &&
      /来[张点个]([涩色瑟]|se)图|再来一张|來[張點個]([澀色瑟]|se)圖/.test(text)
    ) {
      const match = text.match(/(\d)(?:.*\b(\d))?/);
      const start = Number(match?.[1] ?? 2), end = Number(match?.[2] ?? match?.[1] ?? 6);
      const picture = nsfw.randomPicture(start, end);
      if (picture) {
        const sent = await telegram.sendPhoto(message.chat.id, picture.fileId, {
          reply_parameters: { message_id: message.message_id },
          reply_markup: nsfw.ratingKeyboard(picture.fileUniqueId),
        });
        currentInteraction(ctx).aiContext(
          "gallery",
          "已从图库发送一张图片。",
          "success",
          sent.messageId,
        );
        return;
      }
      await currentInteraction(ctx).reply("没有涩图~");
      currentInteraction(ctx).aiContext(
        "gallery",
        `图库评分区间 ${start}–${end} 内没有图片。`,
        "failure",
      );
    }
  });

  async function downloadFromCallback(
    ctx: Context,
    source: string,
    chatId: number,
    messageId: number,
  ): Promise<void> {
    await ctx.answerCallbackQuery({ text: "开始下载" });
    try {
      await downloads.deliver({
        key: await parseDownloadKey(source, false),
        chatId,
        messageId,
        userId: ctx.from!.id,
        userName: [ctx.from!.first_name, ctx.from!.last_name].filter(Boolean).join(" "),
        signal: shutdown.signal,
        // Inline callbacks have no ctx.chat; the download's bound target supplies the notice.
        notice: ctx.chat ? downloadNotice(ctx) : undefined,
      });
      stats.increment(chatId, "download_video");
    } catch (error) {
      logger("download").warn("callback download failed", error);
      const text = error instanceof BilibiliLinkError
        ? error.message
        : "下载失败，请检查链接或稍后重新发起请求。";
      if (ctx.chat) await replyPrivately(ctx, text);
      else {
        await telegram.sendMessage(chatId, text, {
          reply_parameters: { message_id: messageId },
        });
      }
    }
  }

  bot.on("callback_query:data", async (ctx) => {
    const callback = ctx.callbackQuery;
    const data = callback.data;
    const message = callback.message;
    if (!message && callback.inline_message_id && data.startsWith("il:bili:")) {
      const uid = parseBilibiliInlineId(data);
      const context = uid === undefined ? undefined : downloads.getInlineContext(uid, now());
      if (!context) {
        return await ctx.answerCallbackQuery({
          text: "下载上下文已过期，请重新发送 inline 消息",
          show_alert: true,
        });
      }
      if (!context.text || !context.chatId || !context.messageId) {
        return await ctx.answerCallbackQuery({
          text: "尚未收到这条 inline 消息，请在 Bot 所在群重新发送链接后下载。",
          show_alert: true,
        });
      }
      return await downloadFromCallback(ctx, context.text, context.chatId, context.messageId);
    }
    if (!message) return await ctx.answerCallbackQuery({ text: "消息已不可用", show_alert: true });
    if (
      "ephemeral_message_id" in message && message.ephemeral_message_id &&
      (!("receiver_user" in message) || message.receiver_user?.id !== ctx.from.id)
    ) {
      return await ctx.answerCallbackQuery({
        text: "此临时菜单不属于你，请重新打开。",
        show_alert: true,
      });
    }
    if (
      "ephemeral_message_id" in message && message.ephemeral_message_id &&
      !data.startsWith("aimodel2:") && !data.startsWith("gcfg:")
    ) {
      return await ctx.answerCallbackQuery({
        text: "此临时菜单操作不受支持，请重新打开。",
        show_alert: true,
      });
    }
    if (data === "aiusage") {
      const usage = repository.usageForResponse(message.chat.id, message.message_id);
      await ctx.answerCallbackQuery(
        usage
          ? { text: formatUsage(usage), show_alert: true }
          : { text: "用量记录不存在或尚未写入，请稍后重试。", show_alert: true },
      );
      return;
    }
    if (data.startsWith("aimodel:")) {
      return await ctx.answerCallbackQuery({
        text: "模型菜单已更新，请重新使用 /model",
        show_alert: true,
      });
    }
    if (data.startsWith("aimodel2:")) {
      if (!await canAdminister(ctx, configs.value.god)) {
        return await ctx.answerCallbackQuery({
          text: "只有群主或管理员可以切换模型",
          show_alert: true,
        });
      }
      const selection = parseModelCallback(data);
      if (!selection) return await ctx.answerCallbackQuery({ text: "无效模型", show_alert: true });
      const { sessionId, option } = selection;
      const { model, provider } = option;
      if (option.provider === "deepseek" && !configs.value.deepSeekKey) {
        return await ctx.answerCallbackQuery({ text: "DeepSeek API Key 未配置", show_alert: true });
      }
      if (provider === "sub2api" && !configs.value.sub2apiKey) {
        return await ctx.answerCallbackQuery({ text: "Sub2API API Key 未配置", show_alert: true });
      }
      if (provider === "gemini" && !configs.value.geminiKey) {
        return await ctx.answerCallbackQuery({ text: "Gemini API Key 未配置", show_alert: true });
      }
      if (sessionId) {
        const session = repository.getSession(sessionId);
        if (!session || session.chatId !== message.chat.id) {
          return await ctx.answerCallbackQuery({
            text: "目标历史会话不存在或不属于当前聊天",
            show_alert: true,
          });
        }
        repository.setSessionModel(sessionId, provider, model, now());
        await editMenuText(
          ctx,
          `历史会话 #${sessionId} 已切换到 ${option.label}（影响该会话所有参与者）；下次继续时从本地历史重建。`,
          modelKeyboard(model, provider, sessionId),
        );
      } else {
        repository.setChatModel(message.chat.id, provider, model, now());
        ai.resetCurrent(message.chat.id, topicId(message));
        await editMenuText(
          ctx,
          `当前群已切换到 ${option.label}（影响全群）；本话题下一条 AI 消息开始新会话。`,
          modelKeyboard(model, provider),
        );
      }
      await ctx.answerCallbackQuery({ text: `已切换到 ${option.label}` });
      return;
    }
    if (data === "download:bilibili" || data.startsWith("il:bili:")) {
      // A supplied message is authoritative; a query's stored binding may be from another send.
      return await downloadFromCallback(
        ctx,
        messageText(message),
        message.chat.id,
        message.message_id,
      );
    }
    if (data.startsWith("nsfw:")) {
      const [, ratingText, fileUniqueId] = data.split(":");
      if (!fileUniqueId) return await ctx.answerCallbackQuery({ text: "Bot出现错误" });
      const result = nsfw.rate(fileUniqueId, ctx.from.id, Number(ratingText));
      await telegram.editReplyMarkup(
        message.chat.id,
        message.message_id,
        nsfw.ratingKeyboard(fileUniqueId, data.split(":")[3] ?? ""),
      );
      const text = !result.changed
        ? "您不能重复评分"
        : result.existed
        ? `您的评分已由${result.oldRating}修改为${ratingText}`
        : "评分成功";
      await ctx.answerCallbackQuery({ text, show_alert: !result.changed });
      return;
    }
    if (data.startsWith("gcfg:")) {
      if (!await canAdminister(ctx, configs.value.god)) {
        return await ctx.answerCallbackQuery({
          text: "只有群主或管理员可以修改设置",
          show_alert: true,
        });
      }
      const [, rawField, old] = data.split(":");
      const aliases: Record<string, ConfigField> = {
        auto_calculate: "auto_calculate",
        AutoCalculate: "auto_calculate",
        auto_exchange: "auto_exchange",
        AutoExchange: "auto_exchange",
        auto_check_adult: "auto_check_adult",
        AutoCheckAdult: "auto_check_adult",
        auto_cvt_bili: "auto_cvt_bili",
        AutoCvtBili: "auto_cvt_bili",
        resp_nsfw_msg: "resp_nsfw_msg",
        RespNsfwMsg: "resp_nsfw_msg",
      };
      const field = rawField ? aliases[rawField] : undefined;
      if (!field) {
        return await ctx.answerCallbackQuery({ text: "本版本暂未上线", show_alert: true });
      }
      stats.setConfigField(message.chat.id, field, old !== "T");
      await editMenuMarkup(ctx, configKeyboard(stats, message.chat.id));
      await ctx.answerCallbackQuery({ text: "设置已更新" });
      return;
    }
    await ctx.answerCallbackQuery({ text: "本版本暂未上线", show_alert: true });
  });

  bot.catch((error) => console.error("update failed", error.error));

  let runner: RunnerHandle | undefined;
  return {
    bot,
    flush: () => stats.flush(),
    async serve(signal: AbortSignal): Promise<void> {
      signal.addEventListener("abort", () => shutdown.abort(signal.reason), { once: true });
      const logging = installLogging(() => configs.value.logLevel);
      let http: Deno.HttpServer | undefined;
      let flushTimer: ReturnType<typeof setInterval> | undefined;
      let dailyTimer: ReturnType<typeof setInterval> | undefined;
      const stopLatencyMonitor = startLatencyMonitor();
      try {
        http = startInnerHttp(db, stats, configs.value.aiMediaPath, shutdown.signal, logging);
        albums.cleanup();
        await downloads.checkExecutables();
        await registerCommandMenus(bot.api);
        if (configs.value.dropPendingUpdates) {
          await bot.api.getUpdates({ offset: -1, limit: 1, timeout: 0 });
        }
        flushTimer = setInterval(() => {
          try {
            backgroundTiming("stats.flush", () => stats.flush());
            downloads.cleanupInline(now());
            albums.cleanup();
          } catch (error) {
            console.error("periodic flush failed", error);
          }
        }, 60_000);
        let sentDay = "";
        dailyTimer = setInterval(async () => {
          const current = new Date();
          const day = current.toISOString().slice(0, 10);
          if (current.getHours() !== 8 || sentDay === day) return;
          sentDay = day;
          for (const chatId of configs.value.myChats) {
            try {
              stats.flush();
              await telegram.sendMessage(chatId, stats.dailyMessage(chatId, now() - 86_400), {
                parse_mode: "HTML",
              });
            } catch (error) {
              logger("stats").error("daily statistics failed", chatId, error);
            }
          }
        }, 30_000);
        // Both sides implement the Web AbortSignal API. Their declaration files
        // refer to different copies of the same compatibility type.
        const runnerBot = bot as unknown as Parameters<typeof run>[0];
        runner = run(runnerBot, {
          runner: { fetch: { timeout: 120 } },
          sink: { concurrency: 16 },
        });
        console.log(`bot started @${identity.username}`);
        await new Promise<void>((resolve) => {
          if (signal.aborted) resolve();
          else signal.addEventListener("abort", () => resolve(), { once: true });
        });
      } finally {
        stopLatencyMonitor();
        clearInterval(flushTimer);
        clearInterval(dailyTimer);
        shutdown.abort();
        await runner?.stop();
        await runner?.task();
        await http?.shutdown();
        await http?.finished;
        logging.restore();
        stats.flush();
      }
    },
  };
}
