# RichMessage 输入

检查基线：`fb5da9070112aa5b39b41ba426129d4c9bee710e`（TypeScript master）。

收到的 [RichMessage](https://core.telegram.org/bots/api#richmessage) 正文位于 `blocks`，
不是发送接口的 `markdown/html`，也不保证填充 Message 的 `text/caption`。 入口从 blocks
提取可见文本用于触发判断；AI 上下文保留标题、列表、表格、折叠内容、
代码和链接目标，递归读取图片、视频和动画，沿用 FileUniqueId/FileId 媒体缓存。
当前消息与被回复消息使用相同解析路径，不单独保存完整 Telegram 更新。

回复本 bot 的消息可触发 AI，按 Telegram 发送者 ID 判断。回复其他 bot 的消息时， 仍可通过 `@bot` 或
`@new` 显式触发并引用其富消息。斜杠命令仍排除，未明确触发的普通富消息 不写入 AI
历史，也不调用模型。其他 bot 主动触发时仍经过已有连续 bot 消息保护。

离线回归覆盖回复其他 bot 的富消息时的触发判断、隐藏链接、嵌套媒体、富消息 @bot、
未触发富消息不留存及斜杠命令排除；使用拦截 API 和临时数据库。

## Telegram 随机动画

检查基线为 TypeScript master 的 `64ba10cc8351d67f0aba5c0c1d5a11e856fb842d`。 显式触发 AI
时，当前消息及被回复消息中的 [Telegram Dice](https://core.telegram.org/bots/api#dice)
会转换为玩法说明和服务端实际结果。支持骰子、足球、篮球、飞镖、保龄球和老虎机；普通文本 emoji
照原文传递。单独发送随机动画遵循现有触发规则。
