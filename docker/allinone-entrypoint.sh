#!/bin/bash
# WeKnora 单镜像入口：渲染前端/nginx 运行时配置后交给 supervisord。
set -euo pipefail

# ---------------------------------------------------------------------------
# 1) 同容器进程间地址默认值（compose 里是服务名，这里必须回落环）
# ---------------------------------------------------------------------------
export APP_HOST="${APP_HOST:-127.0.0.1}"
export APP_PORT="${APP_PORT:-8080}"
export APP_SCHEME="${APP_SCHEME:-http}"
export DOCREADER_ADDR="${DOCREADER_ADDR:-127.0.0.1:50051}"
export DOCREADER_IMAGE_OUTPUT_DIR="${DOCREADER_IMAGE_OUTPUT_DIR:-/tmp/docreader}"

# 前端构建期 base 与这里必须一致（Dockerfile 的 VITE_BASE_PATH）。
# 该前缀仅用于"直连容器"与本地验证；经 Orion 网关访问时前缀已被网关剥掉。
BASE_PATH="${WEKNORA_BASE_PATH:-/lolr-kb}"
BASE_PATH="${BASE_PATH%/}"
if [ "$BASE_PATH" = "/" ]; then
    BASE_PATH=""
fi

# ---------------------------------------------------------------------------
# 2) 前端运行时配置 config.js（与 frontend/docker-entrypoint.sh 同口径）
# ---------------------------------------------------------------------------
RUNTIME_DEFAULT_LOCALE=""
case "${DEFAULT_LOCALE:-}" in
    zh-CN|zh-TW|en-US|ru-RU|ko-KR|ja-JP) RUNTIME_DEFAULT_LOCALE="${DEFAULT_LOCALE}" ;;
esac

FILE_MB=${MAX_FILE_SIZE_MB:-100}
SKILL_MB=${MAX_SKILL_BUNDLE_SIZE_MB:-256}
if [ "$SKILL_MB" -lt "$FILE_MB" ] 2>/dev/null; then
    SKILL_MB=$FILE_MB
fi
if [ "$SKILL_MB" -gt 512 ] 2>/dev/null; then
    SKILL_MB=512
fi

BRAND_NAME_VALUE=$(printf '%s' "${BRAND_NAME:-Lolr Ai}" | tr -d '"\\\r\n' | cut -c1-64)

mkdir -p /usr/share/nginx/html
cat > /usr/share/nginx/html/config.js <<EOF
window.__RUNTIME_CONFIG__ = {
  MAX_FILE_SIZE_MB: ${FILE_MB},
  MAX_SKILL_BUNDLE_SIZE_MB: ${SKILL_MB},
  DEFAULT_LOCALE: "${RUNTIME_DEFAULT_LOCALE}",
  BRAND_NAME: "${BRAND_NAME_VALUE}"
};
EOF
chmod 0644 /usr/share/nginx/html/config.js

# ---------------------------------------------------------------------------
# 3) nginx 配置渲染
# ---------------------------------------------------------------------------
export MAX_FILE_SIZE=$((FILE_MB + 1))M
export MAX_SKILL_BUNDLE_SIZE=${SKILL_MB}M

envsubst '${MAX_FILE_SIZE} ${MAX_SKILL_BUNDLE_SIZE} ${APP_HOST} ${APP_PORT} ${APP_SCHEME}' \
    < /etc/nginx/templates/default.conf.template > /etc/nginx/conf.d/default.conf

# 健康检查入口（所有形态都注入，供 kubelet/Orion 探针与负载均衡使用）
envsubst '${APP_HOST} ${APP_PORT} ${APP_SCHEME}' \
    < /etc/nginx/templates/health.inc.template > /etc/nginx/conf.d/weknora-prefix.inc

# 前缀兼容：让容器在"直连 :80/lolr-kb/"与"网关已剥前缀"两种入口下都能工作
if [ -n "$BASE_PATH" ]; then
    export BASE_PATH
    envsubst '${BASE_PATH} ${APP_HOST} ${APP_PORT} ${APP_SCHEME}' \
        < /etc/nginx/templates/prefix.inc.template >> /etc/nginx/conf.d/weknora-prefix.inc
fi

# 把 include 插入 server 块末尾（frontend/nginx.conf 以 server 的右花括号结尾）
if [ "$(tail -n 1 /etc/nginx/conf.d/default.conf | tr -d '[:space:]')" != "}" ]; then
    echo "weknora: unexpected nginx template tail; refusing to start" >&2
    exit 1
fi
sed -i '$i\    include /etc/nginx/conf.d/weknora-prefix.inc;' /etc/nginx/conf.d/default.conf

nginx -t

# ---------------------------------------------------------------------------
# 4) 数据目录与外部依赖等待
# ---------------------------------------------------------------------------
mkdir -p /data/files /tmp/docreader

if [ "${DB_DRIVER:-postgres}" = "postgres" ] && [ -n "${DB_HOST:-}" ]; then
    waited=0
    until pg_isready -q -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "${DB_USER:-postgres}" >/dev/null 2>&1; do
        if [ "$waited" -ge 60 ]; then
            echo "weknora: PostgreSQL ${DB_HOST}:${DB_PORT:-5432} not ready after 120s; starting anyway" >&2
            break
        fi
        waited=$((waited + 1))
        sleep 2
    done
fi

# ---------------------------------------------------------------------------
# 5) 启动时初始化/增量升级数据库
#
# 目标库不存在时先创建，然后按字典序执行 /docker-entrypoint-initdb.d 下的
# *.sh（bash 执行）与 *.sql（psql 执行），与官方 postgres 镜像的约定一致。
#
# 与官方镜像的差别：这里不是"只在空数据目录跑一次"，而是每次启动都做增量
# 比对——已执行过的脚本记录在目标库的台账表里，因此后续版本新增的脚本会
# 在下次启动时自动补跑，老库无需重建。
#
# 台账表（默认 public._weknora_initdb_history）：
#   filename / checksum / baseline / applied_at
#   - 已执行：库内已存在同名记录（checksum 变化只告警，不重跑）
#   - baseline=true：老部署首次启用本机制时，把当前已有脚本登记为"已在位"
#     而不执行，避免对存量数据重放历史脚本。可用
#     WEKNORA_INITDB_BASELINE_EXISTING_DB=false 关掉该保护（脚本必须自身幂等）。
#
# 可调项：
#   WEKNORA_INITDB_ENABLED=false                 彻底关闭本步骤
#   WEKNORA_INITDB_DIR=<path>                    自定义脚本目录（默认 /docker-entrypoint-initdb.d）
#   WEKNORA_INITDB_LEDGER=<table>                台账表名（默认 public._weknora_initdb_history）
#   WEKNORA_INITDB_BASELINE_EXISTING_DB=false    老库也重放全部脚本
# ---------------------------------------------------------------------------

INITDB_LEDGER="${WEKNORA_INITDB_LEDGER:-public._weknora_initdb_history}"

# 目标库上的 psql（连接参数已通过 PG* 环境变量导出）
initdb_psql() {
    psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "${DB_USER:-postgres}" -d "$DB_NAME" "$@"
}

# 拼进 SQL 字符串字面量前的转义
initdb_sql_literal() {
    printf '%s' "$1" | sed "s/'/''/g"
}

initdb_checksum() {
    sha256sum "$1" 2>/dev/null | awk '{print $1}'
}

# 台账写入（同名记录覆盖，脚本改动后仍以最后一次执行的为准）
initdb_record() {
    local name sum baseline
    name="$(initdb_sql_literal "$1")"
    sum="$(initdb_sql_literal "$2")"
    baseline="$3"
    initdb_psql -v ON_ERROR_STOP=1 -q -c \
        "INSERT INTO ${INITDB_LEDGER} (filename, checksum, baseline)
         VALUES ('${name}', '${sum}', ${baseline})
         ON CONFLICT (filename) DO UPDATE
            SET checksum = EXCLUDED.checksum,
                baseline = EXCLUDED.baseline,
                applied_at = now()"
}

init_database() {
    local dir="${WEKNORA_INITDB_DIR:-/docker-entrypoint-initdb.d}"
    local exists db_created=false baseline_mode=false
    local executed=0 skipped=0 baselined=0 changed=0
    local file name sum

    [ "${WEKNORA_INITDB_ENABLED:-true}" = "true" ] || return 0
    [ "${DB_DRIVER:-postgres}" = "postgres" ] || return 0
    [ -n "${DB_HOST:-}" ] && [ -n "${DB_NAME:-}" ] || return 0

    if ! command -v psql >/dev/null 2>&1; then
        echo "weknora: psql not found; skip database init" >&2
        return 0
    fi

    # 库名要拼进 CREATE DATABASE 的标识符里，先挡掉引号/空白等危险字符
    if ! [[ "$DB_NAME" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]]; then
        echo "weknora: database name '$DB_NAME' contains unsupported characters; skip database init" >&2
        return 1
    fi

    # 库名已在上面校验过字符集，可直接拼进 SQL 字面量
    exists="$(psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "${DB_USER:-postgres}" \
        -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'")" || {
        echo "weknora: cannot query database list on ${DB_HOST}:${DB_PORT:-5432}; skip database init" >&2
        return 0
    }

    if [ "$exists" = "1" ]; then
        echo "weknora: database '${DB_NAME}' already exists"
    else
        echo "weknora: database '${DB_NAME}' not found; creating it"
        if ! psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "${DB_USER:-postgres}" \
            -d postgres -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE \"${DB_NAME}\""; then
            echo "weknora: failed to create database '${DB_NAME}' (user '${DB_USER:-postgres}' needs CREATEDB)" >&2
            return 1
        fi
        db_created=true
    fi

    if [ ! -d "$dir" ]; then
        echo "weknora: ${dir} not found; no initdb scripts to run"
        return 0
    fi

    local -a files=()
    while IFS= read -r -d '' file; do
        files+=("$file")
    done < <(find "$dir" -maxdepth 1 -type f \( -name '*.sql' -o -name '*.sh' \) -print0 | sort -z)

    if [ "${#files[@]}" -eq 0 ]; then
        echo "weknora: no *.sql/*.sh found in ${dir}; nothing to do"
        return 0
    fi

    # 台账表：记录哪些脚本已经执行过，支撑增量升级
    if ! initdb_psql -v ON_ERROR_STOP=1 -q -c \
        "CREATE TABLE IF NOT EXISTS ${INITDB_LEDGER} (
            filename   text PRIMARY KEY,
            checksum   text NOT NULL,
            baseline   boolean NOT NULL DEFAULT false,
            applied_at timestamptz NOT NULL DEFAULT now()
        )"; then
        echo "weknora: failed to prepare ledger table ${INITDB_LEDGER}; skip initdb scripts" >&2
        return 1
    fi

    local -A applied=()
    while IFS='|' read -r name sum; do
        [ -n "$name" ] && applied["$name"]="$sum"
    done < <(initdb_psql -tAF'|' -c "SELECT filename, checksum FROM ${INITDB_LEDGER}" 2>/dev/null || true)

    # 存量库 + 空台账：登记基线而不重放，只有之后新增的脚本才会执行
    if [ "${#applied[@]}" -eq 0 ] && [ "$db_created" = false ] &&
        [ "${WEKNORA_INITDB_BASELINE_EXISTING_DB:-true}" = "true" ]; then
        baseline_mode=true
        echo "weknora: ledger is empty on an existing database; recording current scripts as baseline"
    fi

    for file in "${files[@]}"; do
        name="$(basename "$file")"
        sum="$(initdb_checksum "$file")"

        if [ -n "${applied[$name]:-}" ]; then
            if [ "${applied[$name]}" != "$sum" ]; then
                echo "weknora: initdb script ${name} changed after being applied; keeping previous result (add a new script instead)" >&2
                changed=$((changed + 1))
            fi
            skipped=$((skipped + 1))
            continue
        fi

        if [ "$baseline_mode" = true ]; then
            initdb_record "$name" "$sum" true || return 1
            echo "weknora: baseline ${name} (assumed already applied, not executed)"
            baselined=$((baselined + 1))
            continue
        fi

        echo "weknora: running initdb script ${file}"
        case "$file" in
            *.sql)
                initdb_psql -v ON_ERROR_STOP=1 -f "$file" || return 1
                ;;
            *.sh)
                bash "$file" || return 1
                ;;
        esac
        # 执行成功才记账，失败下次启动会重试
        initdb_record "$name" "$sum" false || return 1
        executed=$((executed + 1))
    done

    echo "weknora: initdb done on '${DB_NAME}' (executed=${executed} skipped=${skipped} baseline=${baselined} changed=${changed})"
}

# 导出连接信息：*.sh / *.sql 里可以直接调 psql，无需重复写 -h/-U/-d
export PGHOST="${DB_HOST:-}"
export PGPORT="${DB_PORT:-5432}"
export PGUSER="${DB_USER:-postgres}"
export PGPASSWORD="${DB_PASSWORD:-}"
export PGDATABASE="${DB_NAME:-}"

if [ "${DB_DRIVER:-postgres}" = "postgres" ] && [ -n "${DB_HOST:-}" ]; then
    init_database || {
        echo "weknora: database initialization failed; refusing to start" >&2
        exit 1
    }
fi

# ---------------------------------------------------------------------------
# 6) 交给 supervisord（PID 1）
# ---------------------------------------------------------------------------
exec /opt/supervisor/bin/supervisord -c /etc/supervisor/supervisord.conf
