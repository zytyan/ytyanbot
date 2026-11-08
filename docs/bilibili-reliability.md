# Bilibili 转换与下载修复（2026-10-15）

实现前刷新并检查 TS `9311011596d0f17fd852d50203efa4902b87182f`、Go 封存开发分支
`archive/go/20261013/codex/ai-db-v2` 的 `d395a28545701a5640d91b1ff7057bee236a3f1e`。 对照 Go
helpers/bili/converter.go 和 handlers/convertbili.go；短链无待清参数时不发送
转换结果是沿用的旧行为，本次修正为只要短链成功展开便视为需要转换。

线上日志确认下载失败于 findBilibiliCookies 的第一个相对路径 Deno.stat，抛出 NotCapable。目录迁移后
cwd 是仓库根目录，该相对候选不在编译允许读取的 build 下； 探测尚未到真正存在的
build/bili_cookies.json 就中止。修复优先使用唯一运行根下的
绝对账号文件路径，其他候选的不存在、权限拒绝均继续探测，不扩大编译读取权限。
实际验证生产服务用户可读取账号文件；临时下载验收使用副本，随后删除该凭据副本。

短链支持最多五次跳转、相对 Location、循环检测及整个链十秒期限，关闭中间响应体。 只访问
b23.tv/bili2233.cn，跳转目标限定 Bilibili 域名，不跟随任意外部目标。 HTTP/缺失跳转/JSON
错误返回经过清理的明确错误，不再将未解析短链交给下载工具。
下载入口同时规范化短链别名和普通视频链接，去掉跟踪参数、保留 p/t/start_progress，
非视频链接不提供下载按钮，也不启动视频下载。原消息仅在替换消息已发送后尝试删除，
删除失败不将成功转换标记为失败；转换失败保留原消息并记录/注入精简失败事实。

用户首个短链在服务器和用户端均失效，服务器返回 HTTP 200、JSON code=-404；
修复后直接说明上游错误，请用户重发或提供完整链接。补充短链可展开为
https://www.bilibili.com/video/BV1pmJy6mEre?p=1 ，AV/BV 转换与下载器元数据一致，
aid=117444001732258。

以 tgbotapi 用户实际调用现有 bbdown-go，下载得到 8790147 字节 MP4，ffprobe 验证时长约 252.992
秒，H.264 852×480 视频和 AAC 音频齐全。没有向真实 Telegram 发送验收消息。保持本地 API 的 file://
交付方式，不切换为 Web URL 或 multipart； 已有集成测试验证本地 API JSON 文件
URI、失败不落缓存、成功后缓存及音频路径。 独立编译 smoke 继续验证生产用户、受限权限、模拟
API、媒体及交付；另新增无参数短链、
相对跳转、失效短链、跨域/循环、受限文件探测与自动消息完整流程回归。

## Inline 上下文的 64 位 ID（2026-10-15）

本轮刷新分支后检查 TS `a798656e1565111cb3dd373dfc8b8d7c9346811d`，Go 封存开发分支仍为
`d395a28545701a5640d91b1ff7057bee236a3f1e`。

生产库只读检查发现 inline 上下文最大 UID 为 191315037158114041，超过 JavaScript 安全整数。 导入的 Go
int64 UID 使 SQLite 自增继续使用这个范围。原实现插入空上下文后，按 number 读取 last_insert_rowid()
越界；外层 catch 吞掉错误，最终回答通用的“无法转换”。这也解释了为什么 Telegram answerInlineQuery
成功而转换始终失败，以及库里出现未绑定的空上下文。

临时数据库用同一数量级的 UID 复现：修复前只返回 raw 结果，修复后得到两个精确 ID 的 AV/BV
结果。现在仅将该 ID 按 SQLite TEXT 读出并转为 bigint；回调十六进制解析、消息绑定、上下文
查找全程保持 bigint，验证正数及 int64 上限。不改生产数据库、自增序列或历史记录。

增加 inline.bilibili / inline.context 延迟阶段以及不含原查询内容的异常类型日志。Telegram 回答
移到转换异常捕获之外，避免把交付故障误报为转换故障或重复回答。回归覆盖大 ID 创建、回调编码、 via_bot
消息绑定、上下文读取/到期及 Telegram 回答失败只尝试一次。

## Inline 下载回调（2026-10-15）

本轮刷新分支检查 TS `6f15d0d9f28e090c7775965e3097cda677e54ab6`，Go 开发封存分支仍为
`d395a28545701a5640d91b1ff7057bee236a3f1e`，对照 handlers/ytdlp.go 的 DownloadInlinedBv。

Telegram 的 inline callback 提供 inline_message_id，通常没有普通 message；TS 的全局 message
空值检查先返回“消息已不可用”，没有进入下载处理。只读检查最近成功转换的上下文已绑定 文本、聊天和消息
ID，因此该检查是本次按钮失败的直接原因，与下载器无关。

恢复 Go 的绑定查找路径：在普通 message 检查之前处理 il:bili 回调，精确解析 bigint UID，读取 30
天内上下文，以绑定的 chat_id/msg_id 下载并回复原消息。下载状态和失败提示也使用绑定目标， 避免依赖
inline 回调中不存在的 ctx.chat。继续复用本地 API file://、上传缓存、并发和清理逻辑。 普通回调带
message 时，当前消息的文本和地址为准，防止使用旧绑定把回复送到别的消息。

没有收到 via_bot 消息时，Telegram 不提供可发送媒体的 chat_id；inline_message_id 和 chat_instance
不是 sendVideo 的目标地址。这种未绑定情况明确提示在 Bot 所在群发送链接，不向猜测的群或点击者
私聊发送媒体，不解码 Telegram 的不透明 ID。过期/非法 UID 同样不启动下载。

集成回归覆盖仅含 inline_message_id 的缓存视频交付、交付失败提示、未绑定、过期、非法 UID，以及
普通消息回调使用当前消息目标；所有 API 被拦截，不给真实 Telegram 发送验收消息。
官方约束：https://core.telegram.org/bots/api#callbackquery 。

## 下载工具错误诊断（2026-11-07）

检查基线提交 `3ef4c3801eaf3d05e608c1f587ebee5b504504fa`（master，已有其他未提交改动）。
只读生产日志显示 22:52:35 和 23:09:15 的 inline 下载均在 bbdown-go 退出码 1 后失败， 耗时约 1.5/2.3
秒，没有进入媒体上传。只读绑定记录对应群 -1001170816274 的消息 47337， 来源
BV1MrbK66EWx，用户报告的失败提示为消息 47345。

此前非零退出只保留退出码，stderr 被丢弃，因此历史日志不能进一步确定底层原因。
现将经过白名单提取的错误阶段、数字上游状态及退出码保留到异常与用户提示， 不输出原始
stderr、Cookie、token 或签名媒体 URL。没有自动重试、切换账号或更改生产配置。
回归验证错误提取不泄露敏感字段、失败不上传/不写下载缓存且清理临时目录。
用户随后明确授权复现。使用临时凭据副本及生产用户 tgbotapi 复现：APP 解析成功， 视频下载候选先出现
unexpected EOF，备用候选最终明确报 no space left on device。 `/tmp` 为约 2.9 GiB 的 tmpfs，使用率
100%，检查时仅余约 6 MiB； 生产运行目录所在根磁盘约余 530 GiB。初次以 root
复现未立即报错，改用生产用户后重现快速失败。 CDN 也出现过 403，工具已有去掉 Referer
的恢复逻辑；登录解析成功，不能将此次故障归因于账号失效。

运行入口现将 DownloadService 的临时根设为数据库同目录下的 downloads/，首次使用时以 0700
创建，每个任务仍用独立随机子目录并在成功/失败后清理；B站和 YouTube 音视频共用此路径。
现有生产编译读写权限覆盖该运行目录，无需扩大权限或引入新环境变量。
不清理其他项目临时文件、不移动历史媒体、不更改生产配置。空间不足提示明确说明服务器目录空间不足。
回归覆盖自定义临时目录的成功交付和失败清理；所有 Telegram API 均拦截。 未部署、未重启服务。

授权实测在磁盘上的临时目录运行原版 bbdown-go，仍以 tgbotapi 用户、同一凭据副本、同一视频
BV1MrbK66EWx 和 HEVC 参数执行，最终退出码 0：主 CDN 的 EOF/403 由工具既有备用地址逻辑恢复。 得到
37,912,691 字节 MP4；ffprobe 验证 642.368 秒，852×480 HEVC 视频和 AAC 音频齐全。
诊断目录和凭据副本均已删除，未向 Telegram 发送媒体。这验证避开已满 tmpfs 后原工具可以完整下载。

## 发布及 file_id 复用验收（2026-11-07）

用户明确要求上线，并确认下载媒体只需保留 Telegram file_id。 代码已有上传成功后写
yt_dl_results、finally 删除临时目录、下次按持久化 ID 发送的顺序，
本次不另建媒体库或增加数据库结构。增强回归：视频/音频文件删除后检查数据库 file_id， 重新创建
DownloadService 且禁止调用外部下载/探测工具，仍可成功按 ID 交付。 完整检查和 113 项 unit、62 项
integration 测试通过。 独立候选以生产用户在全新 /tmp 数据库及假 Telegram API 下通过受限权限 smoke：
两次按持久化 ID 发送、不创建下载目录、SIGINT 退出码 0。

首次发布的 20 秒健康窗口短于生产启动数据库校验耗时，已自动恢复旧二进制。 历史正常启动从 systemd
Started 到 bot started 约 56 秒；发布验证窗口调整为 180 秒， 同时要求本地健康接口正常且日志确认
Telegram polling 已启动。 发布目录位于生产
run/deploy-download-*，保存旧二进制、停机数据库备份、候选、 基线
SHA、完整源码补丁及校验/测试/编译日志；运行文件及诊断脚本均不进入 Git。

最终候选已上线：二进制 SHA-256 `f8e81ccf8513d1565ea7ee376e0c7277ab1b1339a97fde0451077498dea455c0`，
生产服务 active/running，NRestarts=0，日志确认 bot started；本地 `/` 和 `/debug/runtime` 均返回
200。 成功发布记录为 run/deploy-download-20261107T154138Z/deployment.json。 旧版停止阶段达到 systemd
90 秒退出期限并由 systemd 清理进程，随后完成停机备份和候选启动；
没有更改服务退出期限、生产配置或数据库结构。没有发送真实 Telegram 验收消息。
