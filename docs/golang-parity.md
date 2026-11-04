# Go 基线同步记录

每个切片重新查询远端，不将以下 SHA 当作冻结基线。

## Bilibili 下载

检查基线：Go `codex/ai-db-v2` `d395a28545701a5640d91b1ff7057bee236a3f1e`； TS 前置提交
`a7803bf6616e225afc41bd471cdcd86d41de8c81`。

改用本机 `/usr/bin/bbdown-go`，沿用 Go 的账户 JSON、工作目录、HEVC 优先、 音频独立下载与 JSON
输出协议；编译权限同步。 复用 Go build 中的
`bili_cookies.json`，不读取或打印账户内容，不提交运行文件。
元信息失败不丢弃已经成功下载的文件；两个并发操作均等待完成，输出路径必须位于临时目录。

验收：参数与输出校验、音频交付、元信息失败、临时目录清理，以及现有下载 singleflight 回归。
真实下载不纳入默认测试。

后续待完成：TS 独立 schema/查询源/只读导入、AI-first 编排、工具、Cloudflare 审核、 AI
反应偏好/多模态、内部 HTTP/备份/诊断与运行验收。

已确认：未触发 AI 的相册不保存 caption，只保存文件索引。

## TS 数据库与只读导入

Go 基线仍为 `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `4ab727a`。 独立 TS V1、规范 SQL
与生成验证工作流、只读 Go V9–V11 导入。保持业务 ID， 逐表校验完整行摘要（含 BLOB）、Token、媒体
hash/size、外键和 integrity； CoC
属性、未使用的相册索引与远端状态归档。目标目录必须全新，失败不发布。
临时库回归验证源文件不变、投票聚合不重复、payload 无损和坏媒体拒绝发布。

## AI-first 编排与用户偏好

Go 基线仍为 `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `46db834`。 传统 handler 后执行 AI
检查；只有明确提及、`@new` 或回复已交付的 AI 回答触发。 `@last`、`@数字`
只控制明确触发后的路由，不独立触发。 沿用 Go 默认新会话、回复续接、`@last` 续接当前会话，跨 topic
引用拒绝续接。 显式传统事实按 chat/topic/实际回复关联保存，以 user 上下文提供，不进入 system。
已有事实阻止同一 update 重跑传统副作用；已生成 Run 重发不下载输入、不重调模型。 回复链中
request-only 上下文进入本次请求；窗口按 50 条裁剪且始终不超过 200，
裁剪后才加载媒体。请求快照在模型调用前保存，媒体保存 hash/size 引用。 `/ai_reaction on|off`
用户全局偏好缺省开启。`%CHAT_ID%` 提示词变量已补齐。

验收：非触发普通消息不进 AI 表，传统回复不触发，同消息 /roll + mention 先执行传统功能，
后续明确回复取回事实，事实不升为系统指令；SDK 交付失败重投和一次计量回归通过。

## Go 配置、图片审核与处理

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `d9046bf`。 Cloudflare Clef-flash 直接
HTTP、0/2/4/6 与 anime 两项 choice 校验、JPEG 768px/128KiB 规范化；
失败不保存分数。安全二次元自动入库，每张相册图片分类且最多回复一次； `/score`
显示真实入库状态，只对入库图片提供投票。随机图库恢复评分区间与多种表达。 Go Cloudflare/OCR YAML
字段直接解析，支持独立 TS database/media 环境覆盖； 相对路径按配置目录解析。serve 在任何 writable
打开前只读确认 TS schema，拒绝误写 Go 库。 图片 codec 使用现有 `/usr/bin/ffmpeg`、`ffprobe`，模板是
Go 源码中的静态功能资源， 不是用户媒体；编译嵌入资源并仅增加必要执行/读取路径权限。

验收：70 项回归、真实本机 FFmpeg 鱼图/prpr/WebP→PNG/审核 JPEG smoke 通过。 实际 Go V11
只读导入演练：98,732 AI messages、46,595 Runs、1,023,120 统计桶， 9,412 个媒体、2,066,541,946
字节；完整行/payload 摘要、Token、媒体 hash/size、外键、integrity 通过。 实际演练报告保存在
/tmp，不提交数据库、媒体或运行文件。

## 传统工具与群设置

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `5631f01`。 Fraction.js
有理数计算器保留排列组合、阶乘、百分数、整数除法、乘方、根号、绝对值和微小值科学计数；
限制表达式与结果规模。恢复汇率别名与有界缓存、Azure OCR/缓存/限速及长文字分段，头像 prpr 缓存、 静态
WebP→PNG、鱼图自动拼接；传统结果可显式注入 AI 回复链。恢复五项非 CoC 群开关，管理员校验。
统计文字长度仅统计 text，与 Go 相同。验收：73 项测试通过，包含数学边界与模拟外部 API、 同消息
/calc + mention 的执行顺序及事实注入。

## 内部接口与运维

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `ba28b9e`。 恢复 loopback:4019 /
BOT_INNER_HTTP=OFF、全路由 token、非 loopback 强制鉴权、受限 JSON body、 Mars/Dio 统计、动态 runtime
日志等级、运行内存指标与 V8 CPU profile（替代 Go pprof 格式）。 `/backupdb` 使用 SQLite backup API
的独立一致性快照，校验完整媒体目录映射、大小和 SHA256， 快照对应的 DB/媒体副本/清单打包为
tar.gz；media=0 明确标记不完整 AI 数据集。 格式允许 breaking change，下载文件扩展名与 Content-Type
同步。超时、取消、临时文件清理与单次打包并发限制。 `media-gc` 默认 dry-run，grace-hours
必填；--apply 须在运行停止时执行，仅移除目录中未登记的旧对象，
不删除数据库历史或有登记的媒体。没有自动执行生产清理。76 项回归与本机真实 tar smoke 通过。

## 本地 Telegram 文件

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `99296f8`。 实际 Go 配置 getMe/getFile
探测发现本地 Bot API 返回绝对路径；TS 原 HTTP 拼接会导致图片能力不可用。 现在本地 API 仅读取当前 Bot
的目录、检查 realpath 防止符号链接越界，并对本地/HTTP 输入进行 20 MiB
流式上限和取消检查；错误不包含带凭据的文件路径。编译增加实际 Bot API 数据根读取权限。 77
项回归通过，真实本地图片下载通过（未发送 Telegram 消息）。

## 多媒体准备与配置一致性

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `bc40c80`。 沿用 Go SUB2API_BASE_URL
覆盖；动画贴纸仅发送 emoji/贴纸标记，不把 TGS 当 WebP。
不支持或超限媒体注明未上传；下载失败明确提示，本次不创建部分 Run、不调用模型。
排行/日报注入精简事实。78 项回归通过。Go 配置真实小请求：官方 Gemini 3.7、DeepSeek V4 成功； Sub2API
模型列表正常，但与 Go 相同的 tiered 生成路径返回 404。用户已确认保留配置并继续切换。

## 相册索引与延迟下载

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `1170b70`。
用户确认七天只记录图片文件索引；普通 caption 不入运行时库。TS V2 新增独立 album_index/album_files，
V1 源码和 checksum 保持不变。明确触发后 idle=1s/maxWait=5s 聚合、排序、一次 claim；
回复某张照片（即使 Telegram 省略 group
ID）可取整组。混合组/过期/缺失/任一下载失败或超限拒绝部分输入。 其他照片 caption 不采集；当前触发
caption 或被选中回复 caption 才成为请求数据。 Go 原索引与历史 caption
完整归档，另校验并映射可用文件索引；Go 未记录 topic/unique ID/size， 以
topic=-1（未知）和缺失值保留，不伪造信息。未知 topic 只由 Telegram chat/group/message 身份选取。
新索引包含明确 topic；同 chat 不同 topic 隔离。79 项测试通过，包括真实 App 并发相册、 无 caption
采集、后续回复整组、部分读取失败和导入映射全行摘要。

2026-11-04 用户修订：相册索引不设置七天过期时间；运行时保留记录，直到后续明确策略改变。

## 最终运行验收

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `a80ed3c`。 补齐 V8 heap
snapshot、配置热重载日志等级与临时下载清理/取消；编译明确授予 inspector， 默认 unit/integration
仍保持 hostname 与原文件/环境权限。当前 79 项回归全部通过。 全新 /tmp 独立编译 smoke：TS V2
初始化/只读检查、假 Telegram polling、实际 FFmpeg 鱼图/prpr/WebP→PNG 交付、CPU/heap 诊断、完整
tar.gz 备份、SIGINT 状态 0 退出均通过。 真实导入副本的 Gemini/Sub2API/DeepSeek
历史经拦截重放通过；官方原生与 Interactions、DeepSeek 真实小请求均成功。Sub2API 404
按用户确认保留。用户授权最终停止 Go、只读导入并直接替换当前服务，
运行配置、数据库、媒体、二进制与实际部署记录不进入 Git。

## 媒体原子发布补充

Go `d395a28545701a5640d91b1ff7057bee236a3f1e`；TS 前置 `d0fc9aa`。 部署检查补足 Deno file.write
可能短写的处理：完整写入并 fsync 后才原子 rename，
无进展拒绝发布。强制每次只写两字节的临时文件回归通过；全仓 80 项测试通过。
最终部署二进制重新编译。真实 tgbotapi 用户的独立程序图片、诊断、备份与正常退出验收通过， PATH 中无
Deno/npm；生产 Go 在全部预检查期间保持运行。

## 已完成生产替换（2026-10-12）

部署代码 `c9407581fed721a7fe1faf74dae43d572f457f58`，Go 基线仍为
`d395a28545701a5640d91b1ff7057bee236a3f1e`。先停止 Go，再使用受限权限独立二进制只读导入； 原 Go
数据库前后 SHA-256 完全一致。TS V2 integrity=ok、外键错误=0，媒体 9,413 个、 2,066,628,230
字节。保留 AI messages 98,771、Runs 46,614、sessions 10,430、 统计桶 1,029,456；历史
caption/远端状态/98 份 CoC 属性完整归档，相册文件索引映射通过。 Token：input 174,546,499、output
34,843,760、cached 15,636,609。

`ytyanbot.service` 已经通过单独 override 改为 TS，继续使用原配置和运行用户，
数据库/媒体环境变量指向新数据集。原 Go 程序、配置和源库保留；没有并行 polling。
启动日志和运行指标确认 Deno 正常运行、重启次数 0、近期失败行数 0。
具体部署/迁移报告、源库与二进制哈希及回退步骤保存在被 Git 忽略的 run 目录。 Sub2API 当前 404
按用户明确确认保留配置；官方 Gemini/Interactions、DeepSeek、OCR、Cloudflare 和本地 Telegram
文件已实测。下载默认回归拦截外部 API，工具本机预检通过。

## 回复 AI 无需再次 mention（2026-10-12）

本次刷新 Go 分支头仍为 `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置为
`1dc2525bfe673908942a90e0398588c1e17f283f`。普通群聊回复可能带 message_thread_id，
但并非论坛话题；旧触发查询使用该 ID，而 AI 会话保存为 topic=0，导致无法识别已交付的 AI 回答。
统一沿用 Go newTopic 的 is_topic_message 判断，触发、传统事实、相册和会话控制使用相同话题键。
已复现修复前无需 mention 的回复不调用模型；回归验证修复后继续原会话、保留传统事实，
论坛同话题回复可续聊、跨话题回复不触发，回复传统消息仍不触发。无需数据库迁移。

## 日报标题日期（2026-10-13）

Go 分支头 `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `e53b388`。
日报标题按发送时刻和群时区显示当天，正文仍查询昨日统计，消除统计日重复减时区的问题。 临时数据库覆盖
UTC+8、UTC-5 的跨日标题和昨日计数。

## AI 输入 entities（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `e9cd95f`。 明确触发的当前/回复消息保留
entities 与 caption_entities 的链接、强调、引用和代码语言。 UTF-16 偏移、嵌套格式、代码围栏和隐藏
URL 经过回归；原始文本仍用于命令和触发判断， 普通聊天不进入 AI 历史。SDK 请求与续聊测试确认隐藏 URL
完整保留。

## 下载等待提示（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `7d8d55e`。
所有命令和按钮下载超过一秒才发送回复原请求的等待提示；缓存命中和快速下载不额外发送。
完成、失败、取消后清理提示，包含提示发送晚于下载完成的竞争情况；提示失败不重试、不影响下载结果。
提示不注入 AI 事实。按钮下载也传入服务关闭信号。

## 分模块日志等级（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `ceca9b8`。 恢复
runtime、ai、download、moderation、stats、inner-http 分别设置日志等级。 GET /loggers
列出当前有效等级，PUT /loggers/<name>/<level> 修改单模块；未覆盖的模块继承全局及配置热重载。 AI
日志只写 Run/provider/model 元数据，不写消息正文。诊断和备份格式仍沿用已确认的 V8/tar.gz。

## 用户确认恢复 Go AI 策略（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `f40b78a`。
按用户明确选择覆盖此前约定：回复任意本 bot 消息可触发，Go 子串 mention、斜杠命令排除、1000 UTF-8
字节上限； 同群允许跨 topic 回复/会话 ID 续接，跨群仍拒绝。传统事实取被回复消息自身 topic，仍作为
user 数据。 总生成期限 15 分钟；SDK 禁用自动重试，408/429/5xx/SDK 标记的网络错误按 Go
式退避，生成与兼容恢复共享五次上限。 成功空结果的 payload/usage
保留，不重复生成以免丢失已计费成功请求。Telegram 交付和传统副作用不重试。 AGENTS、AI-first 计划和
SDK 验收记录同步更新。

## Token 口径和展示（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `510dcb6`。 只读核对新请求、原始
usage、SDK 转换和会话累计；详细结论见 token-accounting.md。 输入偏小主要出现在 Interactions
的接口上报与未文档化原始参考值不一致，输出包含推理；
不擅自把参考字段替换成计费用量。用量按钮显示整数、推理拆分、协议和必要原始参考，缓存标为输入子集。
规范 SQL UsageForResponse 读取持久化的原始响应，兼容缺少 raw usage 的历史记录；不改变数据库用量。
回归包含真实现象的脱敏数值和工具输入统计，确保 SDK 已包含的推理不重复相加。

## AI typing 状态（2026-10-13）

刷新 Go `d395a28545701a5640d91b1ff7057bee236a3f1e`，TS 前置 `9e21aaa`。 AI
准备媒体、等待会话锁、生成和交付期间立即发送 typing，随后每三秒续期。 动作归属当前请求的实际 forum
topic，而非被续接历史会话的 topic。
完成、失败、取消停止定时器并中止未完成状态请求；单请求不重叠发送，状态失败不阻止 AI。
非触发消息、传统命令和已交付的重复请求不发送 typing。

## 本地 API 下载文件发送修复（2026-10-13）

TS 前置 7a118ed；封存 Go archive/go/20261013/codex/ai-db-v2 为 d395a28。 线上 B 站 bbdown-go
下载成功，但 sendVideo 报 InputFile.toRaw TypeError， 根因为固定 grammY Web 版本不能把路径字符串作为
InputFile 二进制来源。 按照用户要求，本机 loopback Bot API 的路径型图片/视频/音频直接发送标准
file:// URI， 由 API 读取共享文件，不进行 multipart 上传。URI 编码保留空格、中文、百分号和 #
的文件名语义。 文件保留至 API 成功返回，再记录 file_id、复用缓存、清理临时目录；不重试发送副作用。
真实服务均为 tgbotapi/tgbots，PrivateTmp=no、RootDirectory 为空，能够共享 /tmp。 远端 API
的路径型上传以 64 KiB 有界异步文件读取替代无效字符串 InputFile； 已有 file_id 与内存图片保持原路径。
回归经过真实 grammY 请求编码，验证本地 JSON file URI、远端 multipart 字节， 以及 B
站视频/音频交付、缓存命中、API 读文件时文件存在和完成后的临时清理。
