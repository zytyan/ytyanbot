# ytyango

吹水群自用的 Telegram 机器人，主要面向群聊统计、图片处理、视频下载、AI 对话和一些娱乐/工具类命令。

## 主要功能

- 群聊统计：统计发言、图片等数据，并支持定时发送统计结果。
- 视频与音频下载：支持 B 站链接识别/转换、B 站视频下载，以及 YouTube 视频/音频下载。
- 图片处理：Azure OCR、成人内容检测、WebP 转 PNG、生成 prpr 和萨卡班甲鱼表情。
- 小工具命令：计算器、汇率换算、好好说话、roll 点、COC/DND 骰子与战斗辅助。
- AI 对话：支持 Gemini/DeepSeek、会话、系统提示词、群级模型切换和单次 Token 用量。

## 项目结构

```text
.
├── main.go                 # Telegram bot 入口，注册命令和消息处理器
├── config.example.yaml     # 配置样例，测试环境也会读取它
├── handlers/               # Bot 命令、消息、回调处理逻辑
├── handlers/genbot/        # Gemini/DeepSeek 对话、模型和用量相关逻辑
├── helpers/                # OCR、Bili、图片、数学表达式等辅助模块
├── globalcfg/              # 配置、日志、SQLite 连接和 sqlc 查询封装
├── sql/                    # SQLite schema/query 与 sqlc 插件
├── cmd/                    # 数据库初始化、迁移和归档命令
├── internal/               # 数据库迁移与内部实现
└── manage.sh               # 构建、安装 systemd 服务、查看日志的辅助脚本
```

## 运行环境

- Go 1.26.1 或兼容版本
- SQLite3 运行环境
- 可选：本地 Telegram Bot API 服务，配置项为 `tg-api-url`
- 可选：Azure OCR / Content Moderator，用于图片 OCR 和 NSFW 检测
- 可选：Gemini 和 DeepSeek API Key，用于 AI 对话

## 配置

复制配置样例并按需修改：

```sh
cp config.example.yaml config.yaml
```

默认会读取当前工作目录下的 `config.yaml`。也可以用环境变量指定配置文件：

```sh
YTYAN_CONFIG_FILE=/path/to/config.yaml go run .
```

常用配置项：

- `bot-token`：Telegram Bot Token。
- `god`：机器人管理员用户 ID。
- `my-chats`：启用部分群聊功能的群 ID 列表。
- `ai-chats`：允许 Gemini 对话功能的群 ID 列表。
- `tg-api-url`：Telegram Bot API 地址，例如本地 `telegram-bot-api` 服务。
- `database-path`：主 SQLite 数据库路径。
- `ai-media-path`：AI 多媒体内容寻址目录；默认位于主数据库旁的 `ai-media/`。
- `ocr` / `content-moderator`：Azure 服务配置。
- `gemini-key`：Gemini API Key。
- `deepseek-key`：DeepSeek API Key；也可通过 `DEEPSEEK_API_KEY` 环境变量提供。
- `deepseek-base-url`：DeepSeek API 地址，默认 `https://api.deepseek.com`。
- `drop-pending-updates`：启动时是否丢弃 Telegram 未处理更新。

AI 聊天可使用 `/change_model`（或 `/model`）切换当前聊天模型，使用 `/show_usage` 开关每条 AI 回复下方的 Token 用量按钮。群聊中这些配置仅允许群主、管理员或 `god` 修改。

注意：`config.example.yaml` 中的 token 和 key 仅用于示例/测试占位，实际部署时请使用自己的密钥，并避免提交真实配置。

## 本地开发

安装 Go 依赖并运行：

```sh
go mod download
go run .
```

构建二进制：

```sh
go build -tags=jsoniter -o build/ytyan-go
```

运行测试：

```sh
go test ./...
```

测试环境会自动使用 `config.example.yaml`，并将 SQLite 数据库初始化为内存库，因此通常不需要手动准备数据库文件。

真实外部 API 测试默认跳过，并在测试进程中各打印一次启用方式：

```sh
# Gemini 与 DeepSeek API、缓存和工具回放测试
YTYAN_TEST_AI_API=1 YTYAN_CONFIG_FILE=/path/to/config.yaml go test ./handlers/genbot -run '^TestLive' -v

# Bilibili/b23.tv 短链重定向测试
YTYAN_TEST_BILIBILI_SHORTLINK=1 go test ./helpers/bili -v
```

未设置对应变量时，`go test ./...` 不会访问这些真实外部服务；本地 `httptest` 和纯解析测试不受影响。

## 前端

前端位于 `http/frontend`：

```sh
cd http/frontend
npm install
npm run dev
```

常用脚本：

- `npm run dev`：启动开发服务器。
- `npm run build`：构建静态产物。
- `npm run check`：运行 Svelte 类型检查。
- `npm run lint`：运行格式与 ESLint 检查。

## 部署

仓库提供了 `manage.sh` 作为常用部署辅助：

```sh
./manage.sh build --no-pull
./manage.sh install
./manage.sh restart
./manage.sh log -f
```

脚本会将产物构建到 `build/ytyan-go`，并使用仓库中的 `ytyanbot.service` 模板安装同名 systemd 服务。正式部署前请确认 `build/config.yaml`、数据库路径、日志路径和运行用户权限符合服务器环境。

## 数据库与代码生成

- SQLite schema 和查询定义位于 `sql/`。
- sqlc 配置位于 `sqlc.yaml`。
- 通用 AI V2 schema/query 生成到 `globalcfg/aiq`；其他业务查询生成到 `globalcfg/q`。
- AI 请求先保存为 `pending` Run，再分别记录模型生成与 Telegram 投递结果；失败投递会保留模型载荷用于恢复或重发。
- `go run ./cmd/db-init -output <new.db>` 只用于创建不存在的 canonical 空库。
- AI V1 到 V2 使用 `cmd/ai-db-migrate`；已经完成 V2 的数据库后续离线重写使用 `cmd/main-db-migrate`。两个迁移命令都只读源库并要求输出路径不存在。
- 下线旧消息搜索前，先生成 Meilisearch dump，再用 `cmd/legacy-message-archive` 一致性归档消息库、WAL 和 dump；命令要求全新输出目录并记录行数、完整性和 SHA-256。
- 若已确认 Meilisearch 服务、数据目录和历史 dump 均不存在，可显式使用 `-meili-dump-unavailable-reason` 将原因写入 Manifest；不得用空文件伪装 dump。
- 普通启动只执行小型在线迁移；存在未应用的 offline migration 时会拒绝启动。
- 主库 V4 会离线压缩用户维表并删除退役群资料/消息搜索配置；必须通过 `cmd/main-db-migrate` 生成新库，不能由服务启动隐式执行。
- 主库 V5 会离线解码群统计 Gob 数据：每日标量保留在 `chat_stat_daily`，用户统计和 144 个十分钟桶分别迁入结构化子表，并在逐项校验通过后移除旧 BLOB 列。
- Bilibili inline 下载上下文有效期为 30 天，程序每天清理过期记录；旧记录在 V8 迁移时以迁移时间回填。
- 图片评分 V9 是离线迁移：迁移器会先报告并拒绝越界评分，不自动修正数据；合法数据原样重建并启用评分范围约束与级联删除。

如果修改了 SQL schema 或 query，需要同步更新/重新生成对应 sqlc 产物，并补充相关测试。

`/backupdb` 默认保持仅备份 SQLite 数据库的行为，Manifest 会将其标记为不完整的 AI 数据集。使用 `/backupdb?db=main&media=1`（或 `db=all`）会按一致的主库快照引用打包 `ai-media/` 内容寻址对象，同时写入 `media-manifest.tsv`、对象数量、总字节数和清单 SHA-256。完整媒体备份的最长执行时间可通过 `GOYTYAN_BACKUP_MAX_DURATION` 配置（Go duration 格式，默认 `30m`），客户端取消请求也会终止备份。

内部 HTTP 服务默认只监听 `127.0.0.1:4019`。设置 `GOYTYAN_BACKUP_TOKEN` 后，`/backupdb`、统计写入、logger 管理和 pprof 等全部端点都需要通过 `X-Backup-Token` 请求头（或兼容的 `token` 查询参数）认证；`BOT_INNER_HTTP` 配置为非回环地址时，如果没有设置该 token，服务会拒绝监听。

AI V1 → V2 必须使用离线工具，候选程序会拒绝直接打开尚未完成 V3 迁移的旧库：

```bash
go build -o /tmp/ai-db-migrate ./cmd/ai-db-migrate
/tmp/ai-db-migrate \
  -source /path/to/source.db \
  -output /path/to/new.db \
  -media /path/to/new-ai-media \
  -manifest /path/to/migration-manifest.json
```

工具仅以只读方式打开源库，先通过 SQLite Backup API 创建 staging 副本；迁移、旧 AI 表删除和 `VACUUM INTO` 均发生在副本上。目标数据库、媒体目录和 Manifest 必须预先不存在。正式迁移前必须停止写入并另做可回滚备份；工具生成后仍需检查 Manifest、`integrity_check`、外键、媒体哈希和业务验收，再执行原子切换。

## 开发提示

- 根目录 `README.md` 介绍整体项目和数据库维护命令。
- 图片 OCR、NSFW 检测、Gemini、视频下载等外部服务依赖配置项可能需要真实凭证或本地服务。
- 生产环境建议将真实配置文件、SQLite 数据库、日志和下载缓存放在 `build/` 或独立数据目录中，并加入备份策略。
