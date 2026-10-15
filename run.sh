#!/usr/bin/env bash
# WeKnora 源码启动脚本（go run，日志直接输出到控制台）
#
#   ./run.sh            # 前台启动，Ctrl+C 停止
#   ./run.sh restart    # 先停掉残留实例，再前台启动
#   ./run.sh stop       # 停止正在运行的服务（可另开终端执行）
#   ./run.sh status     # 查看状态
#   ./run.sh web        # 前台启动 frontend 开发服务器（npm run dev，默认 http://localhost:5173）
#
# 等价于：
#   cd <repo> && set -a && . ./.env && set +a && go run ./cmd/server
#   cd <repo>/frontend && npm run dev

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

PID_FILE="$ROOT/.weknora.pid"
PORT="${PORT:-8080}"
MAIN_PKG="./cmd/server"

log() { printf '%s\n' "$*"; }

# 查找可用的 npm：PATH 优先，其次 nvm / 常见安装位置
find_npm() {
    if command -v npm >/dev/null 2>&1; then
        command -v npm
        return 0
    fi
    local c
    for c in "$HOME"/.nvm/versions/node/*/bin/npm /usr/local/bin/npm /usr/bin/npm; do
        if [ -x "$c" ]; then echo "$c"; return 0; fi
    done
    return 1
}

# 查找可用的 go：PATH 优先，其次常见安装位置
find_go() {
    if command -v go >/dev/null 2>&1; then
        command -v go
        return 0
    fi
    local c
    for c in "$HOME/.local/go/bin/go" "$HOME/go/bin/go" /usr/local/go/bin/go /opt/go/bin/go; do
        if [ -x "$c" ]; then echo "$c"; return 0; fi
    done
    # golang.org/dl 安装的版本化 SDK（如 ~/.local/share/go1.26.9）
    for c in "$HOME"/.local/share/go*/bin/go "$HOME"/sdk/go*/bin/go; do
        if [ -x "$c" ]; then echo "$c"; return 0; fi
    done
    return 1
}

# 端口 8080 上正在监听的进程 PID
port_pids() {
    ss -ltnpH 2>/dev/null | grep -F ":$PORT " | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u
}

pids_running() {
    {
        if [ -f "$PID_FILE" ]; then
            local p
            p="$(cat "$PID_FILE" 2>/dev/null || true)"
            [ -n "$p" ] && kill -0 "$p" 2>/dev/null && echo "$p"
        fi
        port_pids
    } | sort -u
}

load_env() {
    if [ ! -f "$ROOT/.env" ]; then
        log "错误: 缺少 $ROOT/.env"
        return 1
    fi

    set -a
    # shellcheck disable=SC1091
    . "$ROOT/.env"
    set +a

    # .env 里是容器内路径，本地运行改为仓库内可写目录
    if [ -z "${LOCAL_STORAGE_BASE_DIR:-}" ] || [ "$LOCAL_STORAGE_BASE_DIR" = "/data/files" ]; then
        export LOCAL_STORAGE_BASE_DIR="$ROOT/.local-data/files"
    fi
    mkdir -p "$LOCAL_STORAGE_BASE_DIR"

    # 与 scripts/dev.sh 保持一致
    export CGO_CFLAGS="${CGO_CFLAGS:--Wno-deprecated-declarations -Wno-gnu-folding-constant}"
}

# 国内网络兜底：仅当未自定义 GOPROXY（仍是官方默认/为空）时注入国内镜像
ensure_goproxy() {
    local go_bin="$1" cur
    cur="$("$go_bin" env GOPROXY 2>/dev/null || true)"
    case "$cur" in
        ""|"https://proxy.golang.org"*|"https://proxy.golang.org/,direct")
            export GOPROXY="https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct"
            export GOSUMDB=off
            log "Go 代理:        $GOPROXY  (GOSUMDB=off)"
            ;;
    esac
}

stop() {
    local pids
    pids="$(pids_running)"
    if [ -z "$pids" ]; then
        log "WeKnora 未在运行"
        rm -f "$PID_FILE"
        return 0
    fi

    log "正在停止 WeKnora (PID: $(echo "$pids" | tr '\n' ' ')) ..."
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null

    local i
    for i in $(seq 1 20); do
        [ -z "$(pids_running)" ] && break
        sleep 1
    done

    pids="$(pids_running)"
    if [ -n "$pids" ]; then
        log "优雅停止超时，强制结束"
        # shellcheck disable=SC2086
        kill -9 $pids 2>/dev/null
        sleep 1
    fi

    rm -f "$PID_FILE"
    log "已停止"
}

start() {
    local go_bin running
    if ! go_bin="$(find_go)"; then
        log "错误: 未找到 go 命令。跑源码需要 Go 1.26（go.mod 要求）。"
        log "可选安装（无需 sudo）:"
        log "  mkdir -p ~/.local/go && curl -fL https://mirrors.aliyun.com/golang/go1.26.0.linux-amd64.tar.gz | tar -xz -C ~/.local/go --strip-components=1"
        return 1
    fi

    running="$(pids_running)"
    if [ -n "$running" ]; then
        log "检测到已在运行 (PID: $(echo "$running" | tr '\n' ' '))，先停止..."
        stop
    fi

    load_env || return 1
    ensure_goproxy "$go_bin"

    log "Go:            $go_bin ($("$go_bin" version 2>/dev/null | awk '{print $3}'))"
    log "Docs 存储目录: $LOCAL_STORAGE_BASE_DIR"
    log "启动: $go_bin run $MAIN_PKG   (Ctrl+C 停止)"
    log "--------------------------------------------------------------"

    # 记录 PID，便于在另一个终端执行 ./run.sh stop
    echo "$$" > "$PID_FILE"
    trap 'rm -f "$PID_FILE"' EXIT INT TERM

    exec "$go_bin" run "$MAIN_PKG"
}

status() {
    local pids
    pids="$(pids_running)"
    if [ -z "$pids" ]; then
        log "WeKnora: 未运行"
        return 1
    fi
    log "WeKnora: 运行中 (PID: $(echo "$pids" | tr '\n' ' '))"
    curl -sf -m 3 "http://127.0.0.1:${PORT}/health" && echo
}

# 前台启动前端开发服务器：cd frontend && npm run dev
web() {
    local npm_bin
    if [ ! -f "$ROOT/frontend/package.json" ]; then
        log "错误: 未找到 $ROOT/frontend/package.json"
        return 1
    fi

    if ! npm_bin="$(find_npm)"; then
        log "错误: 未找到 npm 命令。前端需要 Node.js（建议 Node 20+）。"
        return 1
    fi

    cd "$ROOT/frontend" || return 1

    if [ ! -d node_modules ]; then
        log "警告: $ROOT/frontend/node_modules 不存在，请先执行:"
        log "  cd frontend && npm install"
        return 1
    fi

    log "npm:           $npm_bin ($("$npm_bin" -v 2>/dev/null))"
    log "工作目录:      $PWD"
    log "启动: $npm_bin run dev   (Ctrl+C 停止，默认 http://localhost:5173)"
    log "--------------------------------------------------------------"

    exec "$npm_bin" run dev
}

case "${1:-start}" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; start ;;
    status)  status ;;
    web)     web ;;
    *)       log "用法: ./run.sh [start|stop|restart|status|web]" ;;
esac
