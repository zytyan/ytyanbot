#!/usr/bin/env bash
set -e

SERVICE_NAME=ytyanbot
BUILD_DIR=build
EXEC_NAME=ytyan-go
USER=tgbotapi
GROUP=tgbots
SYSTEMD_FILE="/etc/systemd/system/$SERVICE_NAME.service"
SYSTEMD_TEMPLATE="$(cd "$(dirname "$0")" && pwd)/ytyanbot.service"

function build() {
    local auto_restart=0
    local no_pull=0
    local dry_run=0
    for arg in "$@"; do
        case "$arg" in
            --no-pull) no_pull=1 ;;
            -y) auto_restart=1 ;;
            -n) dry_run=1 ;;
        esac
    done

    [[ "$no_pull" -eq 0 ]] && {
        echo "🔃 Pulling git repo..."
        if [[ "$dry_run" -eq 0 ]]; then
            git clean -fd
        else
            git clean -fdn
            exit 1
        fi
        git pull
    } || echo "🚫 Skipping git pull"

    mkdir -p "$BUILD_DIR"
    go get
    go build -ldflags "-X 'main.compileTime=$(date '+%Y-%m-%d %H:%M:%S')'" -tags=jsoniter -o "$BUILD_DIR/$EXEC_NAME"
    echo "✅ Compile done at $(date '+%Y-%m-%d %H:%M:%S')"

    if [[ "$auto_restart" -eq 1 ]]; then
        echo "🚀 自动重启服务中..."
        sudo systemctl daemon-reload
        sudo systemctl restart "$SERVICE_NAME"
    else
        read -rp "是否要重启服务？[y/n] " isRestart
        [[ "$isRestart" == "y" ]] && {
            sudo systemctl daemon-reload
            sudo systemctl restart "$SERVICE_NAME"
        } || echo "跳过重启。"
    fi
}


function install() {
    echo "🔧 Installing systemd service..."

    if ! id -u "$USER" &>/dev/null; then
        echo "Creating user: $USER"
        sudo groupadd -f "$GROUP"
        sudo useradd -r -g "$GROUP" -d "$(pwd)/$BUILD_DIR" -s /sbin/nologin "$USER"
    else
        echo "User $USER already exists"
    fi

    echo "Setting ownership of $BUILD_DIR to $USER:$GROUP"
    sudo chown -R "$USER:$GROUP" "$BUILD_DIR"

    echo "Installing service file from $SYSTEMD_TEMPLATE to $SYSTEMD_FILE"
    sudo install -m 0644 "$SYSTEMD_TEMPLATE" "$SYSTEMD_FILE"

    sudo systemctl daemon-reload
    sudo systemctl enable "$SERVICE_NAME"
    echo "✅ Installed and enabled $SERVICE_NAME"
}

function control_service() {
    action="$1"
    echo "🔄 $action $SERVICE_NAME"
    sudo systemctl "$action" "$SERVICE_NAME"
}

function view_log() {
    LOG_FILE="$BUILD_DIR/logs/log.log"
    local lines=20
    local follow=0

    for arg in "$@"; do
        case "$arg" in
            -f) follow=1 ;;
            "") ;;
            *)
                if [[ "$arg" =~ ^[0-9]+$ ]]; then
                    lines="$arg"
                else
                    echo "❌ Unknown option: $arg"
                    usage
                    exit 1
                fi
                ;;
        esac
    done

    if ! command -v jq >/dev/null 2>&1; then
        echo "❌ jq 未安装，请先安装 jq 用于格式化日志"
        exit 1
    fi

    if [[ ! -f "$LOG_FILE" ]]; then
        echo "❌ Log file not found: $LOG_FILE"
        exit 1
    fi

    local tail_args=("-n" "$lines")
    [[ "$follow" -eq 1 ]] && tail_args+=("-f")

    # 使用 jq 格式化 JSON 日志，如果行不是合法 JSON 则原样输出
    tail "${tail_args[@]}" "$LOG_FILE" | jq -R 'try fromjson catch .'
}

function usage() {
    cat <<EOF
Usage: $0 <command> [options]

Commands:
  build build/ b [--no-pull] [-y] [-n]        拉取代码并构建项目，支持自动重启服务
  install                            安装 systemd 服务，创建用户组并设定权限
  start | stop | restart | status    控制 systemd 服务状态
  log [N] [-f]                       查看日志，支持 -f 追加模式 和 指定行数，使用jq处理输出
EOF
}

# 主控制逻辑
case "$1" in
    build|build/|b)
        shift
        build "$@"
        ;;
    install)
        install
        ;;
    start|stop|restart|status)
        control_service "$1"
        ;;
    log)
        shift
        view_log "$@"
        ;;
    *)
        usage
        exit 1
        ;;
esac
