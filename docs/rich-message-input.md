# RichMessage 输入

检查基线：`fb5da9070112aa5b39b41ba426129d4c9bee710e`（TypeScript master）。

收到的 [RichMessage](https://core.telegram.org/bots/api#richmessage) 正文位于 `blocks`，
不是发送接口的 `markdown/html`，也不保证填充 Message 的 `text/caption`。 入口从 blocks
提取可见文本用于触发判断；AI 上下文保留标题、列表、表格、折叠内容、
代码和链接目标，递归读取图片、视频和动画，沿用 FileUniqueId/FileId 媒体缓存。
当前消息与被回复消息使用相同解析路径，不单独保存完整 Telegram 更新。

回复任意 `from.is_bot` 消息都会触发 AI，不要求回复本 bot。
斜杠命令仍排除；未明确触发的普通富消息不写入 AI 历史，也不调用模型。 其他 bot
主动触发时仍经过已有连续 bot 消息保护。

离线回归覆盖其他 bot 的富消息回复、隐藏链接、嵌套媒体、富消息 @bot、
未触发富消息不留存及斜杠命令排除；使用拦截 API 和临时数据库。
