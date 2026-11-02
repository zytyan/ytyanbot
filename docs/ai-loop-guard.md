# AI session 防循环

允许机器人消息触发 AI 时，同一 session 连续收到三次机器人提交，从第三次开始只保存输入和媒体，
不调用模型，也不发送 AI 回复或占用 Token。判断来源使用 Telegram `from.is_bot`；本机器人生成的
assistant 回复和仅作为引用加入的消息不计为提交。

后续机器人显式触发仍只保存输入。人类重新显式触发该 session 时清零计数，携带已保存内容恢复生成。
普通聊天不进入 AI 历史，也不清零计数。各 session 独立，沿用现有引用、显式 session 和 chat 隔离规则。

TS migration 005 的 `ai_session_request_origins` 在同一事务内保存提交记录、封顶为 3 的连续计数、
抑制标记及输入；未抑制时一起创建 pending run。抑制的提交不创建 run，不伪造生成或交付状态。 计数在
session 锁内更新，重启后仍有效；重复提交不增加计数，已抑制的旧提交在人类恢复后也不重放。 恢复 Gemini
Interactions 时发送上次 assistant 后完整的未提交输入尾部，保留只入库的机器人内容。

升级前的来源信息不可靠，不对旧记录推断机器人身份；计数从升级后的显式请求开始。 现有手动 session
suspension 独立生效，不能通过人类提交解除。

检查基线：刷新远端后，TS 分支 `codex/retain-features` 为
`4d41db2f6588b099bf19d34526d30c4fd07e285b`，`master` 为
`ac67c5f7651d3e2a4a2225ae4bd3f3a9f455e979`（已是 TS）。Go 仅只读参考归档
`archive/go/20261013/master` 的 `8f5de9b`，没有修改归档源码。

验证：临时数据库和拦截 Telegram/模型 API，覆盖第三次抑制、重建服务、重复投递、并发、 session
隔离、人类恢复、普通聊天不采集、完整尾部与 Token 计数，以及 schema 升级。
