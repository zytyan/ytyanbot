# ytyanbot-ts

`ytyanbot-ts` 是独立的 TypeScript/Deno 重构，在当前 `master` 直接开发并记录检查基线，
不要求逐项刷新或推送分支。当前实现 AI、视频/音频下载与 Bilibili 转换、NSFW 检测和图库评分、
群消息统计/日报，以及已上线功能的群配置。

后续迁移沿用本仓库：**只裁剪 CoC，保留其他能力，但允许数据库与交互 breaking change**。 新 TS
数据库独立设计，旧数据通过离线导入接收；不要求运行时复用 Go 库或逐字兼容旧命令/回复。 AI
上下文仍按回复链，传统功能先执行并通过 `aiContext(...)` 显式注入事实，随后才检查是否触发 AI；
不采集普通群聊历史，不在未触发时调用模型。 功能清单见
[features](docs/features.md)，架构、阶段计划及剩余问题见
[AI-first 计划](docs/ai-first-plan.md)，简化层级与 SDK 评估见
[架构分析](docs/architecture-sdk-analysis.md)。普通 `/roll` 已补齐，包含旧 `delme`
删除按钮兼容和离线测试。

**TS 独立 V8 数据库与一次性离线导入器已实现**：运行时只接受 TS 库，Go canonical V9–V11 通过
`db-import` 只读导入新目标目录，校验行数据、Token、payload、媒体与外键，并输出报告。
导入失败不发布目标、不修改源库。AI-first 编排和传统事实注入已实现。AI 媒体使用 Telegram FileUniqueId
标识，缺失缓存通过 FileId 补下载， 不计算内容哈希；精简存储与旧库离线归档方式见
[数据库说明](docs/database.md)。

## 固定工具链

- Deno `2.9.3`
- grammY `1.45.1` 的 Web/Deno export
- grammY runner `2.0.3` 的官方 Deno entrypoint
- `@std/yaml 1.1.2`、`@std/assert 1.0.19`、`zod 4.4.3`
- Vercel `ai 7.0.127`、`@ai-sdk/google 4.0.87`、`@ai-sdk/deepseek 3.0.58`
- SQLite 使用 Deno `node:sqlite`，无 ORM、FFI 或原生扩展

本机安装的官方 `deno-x86_64-unknown-linux-gnu.zip` SHA-256 为：

```text
8101865641cbede56f08ad19c0a67a87df84bce127fee0d3e3e1f7467717ffa6
```

所有模块版本和远程模块内容均由 `deno.lock` 固定。runner 使用官方 Deno 源码入口，并通过 import map
把它的 grammY 依赖固定到 `1.45.1/web`；这样编译产物无需开放整个环境变量空间。

## 结构

```text
src/
  ai/          provider、会话/Run repository、媒体存储和业务服务
  download/    URL/Bilibili 处理、进程执行、缓存和 singleflight
  nsfw/        Cloudflare Clef-flash、图库和用户评分
  stats/       统计缓存、聚合和日报
  db/          TS V8 schema、只读导入、初始化和严格检查
  telegram/    grammY typed API 与 sendRichMessage 窄适配
  shared/      当前共享类型与有界并发原语（单实现端口计划收拢）
```

AI 直接使用 SDK 模型实例与 `generateText`，不经自建 Provider 接口/类/注册 Map。 其他部分目前使用
Telegram、media、process、clock 端口。后续优先直接使用成熟库，
收拢无实际替换需求的单实现端口，不把这套结构作为目标架构。SQLite 事务内不进行网络请求或异步操作。

## 配置和运行

从示例开始建立测试配置：

```sh
mkdir -p run
cp config.example.yaml run/test-config.yaml
export YTYAN_CONFIG_FILE=/opt/tgbots/ytyanbot/run/test-config.yaml
```

支持 Go 版 YAML 字段，以及 `YTYAN_DATABASE_PATH`、`YTYAN_AI_MEDIA_PATH`、
`YTYAN_BOT_TOKEN`、`GEMINI_API_KEY`、`CLIPROXYAPI_API_KEY`、`CLIPROXYAPI_BASE_URL`、
`DEEPSEEK_API_KEY` 覆盖。Bot token、Telegram API URL、数据库和媒体路径改变后必须重启；
白名单、provider key、NSFW 和日志配置可热重载，无效配置保留旧快照。

模型选择同时保存 provider/model；Gemini 3.1 Flash-Lite 和 Sub2API 入口已移除。CLIProxyAPI 提供
`Claude Sonnet 4.6（CPA）`（`claude-sonnet-4-6`）、
`Claude Opus 5.5（CPA）`（`claude-opus-5-5-high`）、
`Gemini 3.8 Flash（CPA）`（`gemini-3.8-flash-high`）、
`Gemini 4 Hydrogen（CPA）`（`gemini-4-hydrogen`）和
`Gemini 4 Argon（CPA）`（`gemini-4-argon`）。配置独立的 `cliproxyapi-key` 和
`cliproxyapi-base-url`（服务根地址，默认 `http://127.0.0.1:8317`，不含 `/v1` 或 `/v1beta`）；Claude
使用 `/v1/messages`，Gemini 使用 `/v1beta/models/...:generateContent`，不复用官方 Gemini 缓存或
Interactions。旧模型菜单需要重新使用 `/model` 打开。历史 Sub2API 数据保留，
旧会话需先切换到可用模型再继续。代理中的模型 ID/别名需与上述 ID 一致。

`gemini-explicit-cache: true` 的新官方会话使用原生生成；稳定上下文至少 4096 tokens、
最近稳定消息至少五分钟前时才尝试创建 900 秒缓存。复用检查内容/提示词/模型/tools 指纹；
命中后发送整个未缓存尾部，而不是仅发最后一条。已有 Interactions 会话继续使用原协议，
不把签名/工具步骤压成显示文字创建缓存。关闭显式缓存的新官方会话使用 SDK Interactions，
兼容性错误才回退原生路径。

直接调用现有供应商，不经过 Vercel AI Gateway，也不改变现有计费渠道。 SDK
自动重试关闭；每次生成总期限 15
分钟，瞬时错误重试与缓存失效/远端链失效/接口不兼容恢复共享最多五次生成尝试。 原字节 provider
响应、native assistant 与含 providerOptions 的 SDK messages 均持久化； 输出 token 已包含
reasoning，不再次相加。发送失败使用已存结果重发，不重新请求模型。

CLI：

```sh
deno task check
deno task test
deno task compile

./dist/ytyanbot-linux-x86_64 --version
./dist/ytyanbot-linux-x86_64 db-init --database /opt/tgbots/ytyanbot/run/new.db
./dist/ytyanbot-linux-x86_64 db-check --database /path/to/ts.db --media /path/to/ai-media
./dist/ytyanbot-linux-x86_64 db-import --source /path/to/go.db --media /path/to/go-ai-media --output /tmp/new-import
./dist/ytyanbot-linux-x86_64 serve
```

`db-init` 拒绝覆盖已有文件。`db-check` 以 SQLite read-only 模式校验 TS migration、checksum、
必需表/索引/触发器、退役对象、`integrity_check`、外键和可选媒体引用。

编译产物只允许读取 Go build、本地 Bot API 数据根、嵌入图片资源、仓库 `run` 和 `/tmp`，只允许写仓库
`run` 和 `/tmp`， 只允许执行以下程序：

```text
/usr/local/bin/yt-dlp
/usr/bin/bbdown-go
/usr/bin/ffprobe
/usr/bin/ffmpeg
/usr/bin/tar
```

它没有 shell 或 FFI 权限。SDK 根入口的间接 OIDC 模块需要 hostname 权限，因此测试仅补充
`--allow-sys=hostname`；编译额外允许 `inspector` 供 CPU/heap 诊断，不开放全部系统信息或环境变量。
`deploy/ytyanbot-ts-test.service` 仅用于独立测试 Bot，不会替换生产单元。

## 已验证

- `deno fmt --check`
- `deno lint`
- `deno check src/main.ts`
- 无网络/环境权限、仅 hostname 权限的 unit tests
- 仅授予文件/环境权限的 integration tests
- Linux x86_64 独立二进制 `--version`、`db-init`、`db-check` smoke
- 在无 Deno/npm 的 PATH 中连接本地假 Telegram API，runner 预热后空闲 RSS 约 62 MiB
- SIGINT 可停止 runner、关闭配置 watcher 并 flush 后以状态 0 退出

真实 Go 配置已用于 Telegram 身份/本地文件、官方 Gemini 原生/Interactions 与 DeepSeek 小请求验证。
CPA 已通过内部代理的真实两轮续聊验证（Sonnet 4.6、Gemini 3.8 Flash High）， 验证原生协议、历史
payload 和用量统计。默认测试全部拦截外部 API，不使用生产配置。

## 相册与运维

相册只保存文件索引，不采集普通 caption，索引不按时间过期。明确触发后聚合整组并延迟下载；
混合媒体、缺失索引、任一图片失败或超限都拒绝发送残缺相册。 当前请求及选中的回复 caption
才成为请求数据。历史 Go caption 完整归档。

内部 HTTP 缺省 `127.0.0.1:4019`，`BOT_INNER_HTTP=OFF` 关闭； `GOYTYAN_BACKUP_TOKEN`
可保护全部接口，非 loopback 必须设置。 `/mars-counter`、`/dio-ban` 保留外部统计接入；`/loggers` 与
`PUT /loggers/runtime/<level>`
控制运行日志；`/debug/runtime`、`/debug/profile?seconds=1..60`、`/debug/heap` 提供 Deno
指标、Chrome/V8 CPU profile 和 heap snapshot，取代 Go pprof 格式。 `/backupdb?db=main|all&media=0|1`
下载一致性快照的 tar.gz，media=1 按标识/大小校验并附带现有缓存和缺失清单，缓存缺失及 media=0
明确标记非完整 AI 数据集。`GOYTYAN_BACKUP_MAX_DURATION` 支持 ms/s/m/h（缺省 30m）。

`media-gc --database <TS.db> --media <root> --grace-hours <positive>` 默认仅预览； `--apply`
只能在程序停止时使用，仅删除未登记且超过明确宽限期的文件，不删除数据库历史。

数据库规范 SQL 和一次性只读迁移见 [数据库文档](docs/database.md)， 实现 SHA、逐切片验证和实际验收见
[基线同步记录](docs/golang-parity.md)。 CoC 人物属性、检定和战斗不进入运行时；历史数据另行归档。独立
`/roll` 保留。 旧消息搜索与 AI memory 不重新引入。

## 运行目录

当前 TS 仓库及线上运行统一位于 `/opt/tgbots/ytyanbot`。旧 `ytyanbot-ts` 目录已停用，
后续不要再从那里开发或部署。原 Go 的配置和离线数据保留在 `build/`；TS 数据位于 `run/`。

## GitHub 自动构建与发布

提交到 `master` 或提出 PR 时运行检查、测试及 Linux x86_64 构建，成功后可下载 Actions artifact。推送
`v*` 标签（例如 `v1.0.0`）自动创建 Release，包含压缩包、SHA-256
和包内源码提交记录。仓库运行配置、数据库、媒体和凭据不会进入发布包。

也可在 Actions → Build and Release → Run workflow 手动运行；`release_tag`
留空只构建，填入已存在的版本标签才发布该标签对应代码。已有 Release
不自动覆盖，发布需要修改版本标签。无需额外配置个人令牌，发布 job 使用仓库自动提供的 GITHUB_TOKEN。

构建产物面向 Debian 13/Linux x86_64；其他架构和系统版本不作保证。

## 主分支

`master` 现在是正在使用的 TypeScript/Deno 版本。原 Go master 完整保存在
`archive/go-master-20261103`，现有其他 `archive/go/` 分支保持归档，不作为部署或发布来源。
