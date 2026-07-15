# ytyango

吹水群自用的 Telegram 机器人，主要面向群聊消息记录、检索、统计、图片处理、视频下载和一些娱乐/工具类命令。

## 主要功能

- 消息归档与搜索：保存群消息、图片 OCR 结果，并通过 MeiliSearch 提供更适合中文的模糊搜索。
- 群聊统计：统计发言、图片等数据，并支持定时发送统计结果。
- 视频与音频下载：支持 B 站链接识别/转换、B 站视频下载，以及 YouTube 视频/音频下载。
- 图片处理：Azure OCR、成人内容检测、WebP 转 PNG、生成 prpr 和萨卡班甲鱼表情。
- 小工具命令：计算器、汇率换算、好好说话、roll 点、COC/DND 骰子与战斗辅助。
- AI 对话：支持 Gemini/DeepSeek、会话、系统提示词、群级模型切换和单次 Token 用量。
- HTTP 搜索后端：内置 Gin 后端，供前端或 Telegram WebApp 调用搜索、用户信息和头像接口。
- Svelte 前端：`http/frontend` 下提供搜索页面和 OpenAPI 类型封装。

## 项目结构

```text
.
├── main.go                 # Telegram bot 入口，注册命令、消息处理器和 HTTP 服务
├── config.example.yaml     # 配置样例，测试环境也会读取它
├── handlers/               # Bot 命令、消息、回调处理逻辑
├── handlers/genbot/        # Gemini/DeepSeek 对话、模型和用量相关逻辑
├── helpers/                # OCR、MeiliSearch、Bili、图片、数学表达式等辅助模块
├── globalcfg/              # 配置、日志、SQLite 连接和 sqlc 查询封装
├── sql/                    # SQLite schema/query 与 sqlc 插件
├── http/backend/           # Gin HTTP API 后端
├── http/frontend/          # SvelteKit 前端
├── http/openapi.yaml       # HTTP API 契约
├── scripts/                # 数据库迁移脚本
└── manage.sh               # 构建、安装 systemd 服务、查看日志的辅助脚本
```

## 运行环境

- Go 1.26.1 或兼容版本
- SQLite3 运行环境
- 可选：MeiliSearch，用于消息搜索
- 可选：本地 Telegram Bot API 服务，配置项为 `tg-api-url`
- 可选：Azure OCR / Content Moderator，用于图片 OCR 和 NSFW 检测
- 可选：Gemini 和 DeepSeek API Key，用于 AI 对话
- 前端开发需要 Node.js 与 npm

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
- `save-message`：是否保存群消息。
- `database-path`：主 SQLite 数据库路径。
- `ai-media-path`：AI 多媒体内容寻址目录；默认位于主数据库旁的 `ai-media/`。
- `msg-db-path`：消息归档 SQLite 数据库路径。
- `meili-wal-db-path`：MeiliSearch 写入失败时使用的本地 WAL 数据库。
- `meili-config`：MeiliSearch 地址、索引名、主键和 master key。
- `ocr` / `content-moderator`：Azure 服务配置。
- `gemini-key`：Gemini API Key。
- `deepseek-key`：DeepSeek API Key；也可通过 `DEEPSEEK_API_KEY` 环境变量提供。
- `deepseek-base-url`：DeepSeek API 地址，默认 `https://api.deepseek.com`。
- `backend-addr`：内置 HTTP 后端监听地址，默认 `127.0.0.1:4021`。
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

## HTTP API

机器人启动时会同时监听本地 HTTP 后端：

- `POST /search`：搜索消息。
- `POST /users/info`：查询用户信息。
- `GET /users/:userId/avatar`：获取用户头像。

服务默认在 `main.go` 中以 `127.0.0.1:4021` 启动。接口契约见 `http/openapi.yaml`，前端目录也保留了一份同步后的 `http/frontend/openapi.yaml`。

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
- 生成后的 Go 查询代码位于 `globalcfg/q` 和 `globalcfg/msgs`。
- `scripts/migrate_sqlite.py` 可用于将旧 SQLite 数据迁移到当前 schema。

如果修改了 SQL schema 或 query，需要同步更新/重新生成对应 sqlc 产物，并补充相关测试。

## 开发提示

- 根目录 `README.md` 介绍整体项目；`http/frontend/README.md` 是前端脚手架说明。
- 搜索功能依赖 MeiliSearch；如果只开发非搜索逻辑，可以先使用内存数据库和配置样例跑单元测试。
- 图片 OCR、NSFW 检测、Gemini、视频下载等外部服务依赖配置项可能需要真实凭证或本地服务。
- 生产环境建议将真实配置文件、SQLite 数据库、日志和下载缓存放在 `build/` 或独立数据目录中，并加入备份策略。
