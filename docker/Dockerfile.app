# Build extension and daemon from the same pinned source on the runtime architecture.
# --platform is intentionally omitted: a FROM without it already defaults to
# $TARGETPLATFORM, and setting it explicitly triggers BuildKit's
# RedundantTargetPlatform check.
FROM node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e AS browserskill
WORKDIR /build
# Use the same optional Debian mirror as the Go builder and runtime stages.
# Defaults to the Aliyun mirror so plain `docker build` needs no extra args;
# pass --build-arg APK_MIRROR_ARG= to fall back to deb.debian.org.
ARG APK_MIRROR_ARG=mirrors.aliyun.com
# This base image has no CA certificates. Install them from the original HTTP
# Debian source before swapping in a mirror: mirrors.tencent.com redirects
# apt to HTTPS, and that handshake fails without a trust store.
RUN if [ -n "$APK_MIRROR_ARG" ]; then \
        apt-get update && \
        apt-get install -y --no-install-recommends ca-certificates && \
        rm -rf /var/lib/apt/lists/* && \
        sed -i "s@deb.debian.org@${APK_MIRROR_ARG}@g" /etc/apt/sources.list.d/debian.sources; \
    fi && \
    apt-get update && \
    apt-get install -y --no-install-recommends git python3 ca-certificates curl build-essential cmake pkg-config && \
    rm -rf /var/lib/apt/lists/*
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
ENV PATH=/usr/local/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable
COPY scripts/build_browserskill.sh scripts/browserskill-release.json ./scripts/
ARG TARGETOS
ARG TARGETARCH
# BrowserSkill 的源码默认通过 GitHub 加速代理拉取（pinned commit），避免
# 国内网络直连 github.com 抖动；显式传 --build-arg BROWSERSKILL_REPO= 可回退
# 到脚本内默认的官方地址，也可换成任何能代理 git smart HTTP 的地址。
ARG BROWSERSKILL_REPO=https://gh-proxy.com/https://github.com/Tencent/BrowserSkill.git
# 该阶段会用 npx 拉取 pnpm；默认国内 npm 镜像（阿里/淘宝源）。
ARG NPM_REGISTRY=https://registry.npmmirror.com
# 部分网络下 GitHub 的 HTTP/2 framing 会中断（curl 16 / "RPC failed"），
# 统一强制 HTTP/1.1；git 的低速阈值避免长时间挂死。
# NOTE: 用 `git config` 写入 ~/.gitconfig，而不是 GIT_CONFIG_KEY_*/VALUE_* 环境
# 变量——后者会被 BuildKit 的 SecretsUsedInArgOrEnv 检查误判为敏感数据。
ENV BROWSERSKILL_REPO=${BROWSERSKILL_REPO} \
    GIT_HTTP_LOW_SPEED_LIMIT=1000 \
    GIT_HTTP_LOW_SPEED_TIME=30
RUN git config --global http.version HTTP/1.1 && \
    if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && \
    bash scripts/build_browserskill.sh /opt/weknora/browserskill "${TARGETOS}/${TARGETARCH}"

# Build stage
FROM golang:1.26-bookworm AS builder

WORKDIR /app

# 通过构建参数接收敏感信息
# 默认走国内 Go 模块代理与 Debian 镜像；传空字符串可回退到官方源。
ARG GOPRIVATE_ARG
ARG GOPROXY_ARG=https://goproxy.cn,direct
ARG GOSUMDB_ARG=off
ARG APK_MIRROR_ARG=mirrors.aliyun.com

# 设置Go环境变量
ENV GOPRIVATE=${GOPRIVATE_ARG}
ENV GOPROXY=${GOPROXY_ARG}
ENV GOSUMDB=${GOSUMDB_ARG}

# Install dependencies
RUN if [ -n "$APK_MIRROR_ARG" ]; then \
        sed -i "s@deb.debian.org@${APK_MIRROR_ARG}@g" /etc/apt/sources.list.d/debian.sources; \
    fi && \
    apt-get update && \
    apt-get install -y git build-essential libsqlite3-dev curl

# Install migrate tool
RUN go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Copy go mod files. go.mod replace-points anydoc at ./third_party/anydoc-go,
# so that module's go.mod must exist before `go mod download`.
COPY go.mod go.sum ./
COPY third_party/anydoc-go/go.mod third_party/anydoc-go/go.mod
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/download cmd/download
RUN go run cmd/download/duckdb/duckdb.go
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod bash ./scripts/copy-licenses.sh /license-bundle

# Get version and commit info for build injection
ARG VERSION_ARG
ARG COMMIT_ID_ARG
ARG BUILD_TIME_ARG

# Set build-time variables
ENV VERSION=${VERSION_ARG}
ENV COMMIT_ID=${COMMIT_ID_ARG}
ENV BUILD_TIME=${BUILD_TIME_ARG}

# Link the anydoc parser engine (office docs converted in-process, no
# Python docreader). Default on so Hub / compose images ship a working
# engine; pass WITH_ANYDOC=0 to skip the Rust toolchain (~few minutes and
# ~1 GB of build-stage layers).
ARG WITH_ANYDOC=1
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
ENV PATH=/usr/local/cargo/bin:$PATH
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    if [ "$WITH_ANYDOC" = "1" ]; then \
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
            | sh -s -- -y --profile minimal --default-toolchain stable && \
        ./scripts/build-anydoc-lib.sh; \
    fi

# Build the application with version info. Mount the Go build cache as well as
# the module cache: `COPY . .` invalidates this layer on every source change,
# and without the mount each rebuild regenerates the whole compile cache inside
# the layer (~2.7 GB), which occupies disk locally and is uploaded to the CI
# layer cache. Mounted, the cache lives outside the layer and repeated builds
# reuse compiled objects instead of recompiling from scratch. Read the Go
# version in this builder so host machines without Go still report it correctly.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    export GO_VERSION="$(go version)"; \
    if [ "$WITH_ANYDOC" = "1" ]; then \
        make build-prod GO_BUILD_TAGS=anydoc; \
    else \
        make build-prod; \
    fi
RUN --mount=type=cache,target=/go/pkg/mod cp -r /go/pkg/mod/github.com/yanyiwu/ /app/yanyiwu/

# Final stage
FROM debian:12.12-slim

WORKDIR /app

# 默认阿里 Debian 源；传空字符串回退官方源。
ARG APK_MIRROR_ARG=mirrors.aliyun.com
# 运行时 pip 依赖（pip/setuptools/wheel）默认走阿里 PyPI 镜像。
ARG PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple/

# Pairing derives the gateway URL from the user's page origin by default.
ENV BROWSERSKILL_BINARY=/opt/weknora/browserskill/bsk \
    BROWSERSKILL_EXTENSION_PATH=/opt/weknora/browserskill/browser-skill-weknora-0.4.1.zip
COPY --from=browserskill /opt/weknora/browserskill /opt/weknora/browserskill

# Create a non-root user first
RUN useradd -m -s /bin/bash appuser

# First, install ca-certificates without mirror to ensure HTTPS works
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Then switch to mirror if specified and install other packages
RUN if [ -n "$APK_MIRROR_ARG" ]; then \
        sed -i "s@deb.debian.org@${APK_MIRROR_ARG}@g" /etc/apt/sources.list.d/debian.sources; \
    fi && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        build-essential postgresql-client default-mysql-client tzdata sed curl bash vim wget \
        libsqlite3-0 \
        python3 python3-pip python3-dev libffi-dev libssl-dev \
        nodejs npm \
        gosu \
        ffmpeg qpdf && \
    python3 -m pip install --break-system-packages --upgrade pip setuptools wheel \
        -i "$PIP_INDEX_URL" && \
    mkdir -p /home/appuser/.local/bin && \
    curl -LsSf https://astral.sh/uv/install.sh | CARGO_HOME=/home/appuser/.cargo UV_INSTALL_DIR=/home/appuser/.local/bin sh && \
    chown -R appuser:appuser /home/appuser && \
    ln -sf /home/appuser/.local/bin/uvx /usr/local/bin/uvx && \
    chmod +x /usr/local/bin/uvx && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Create data directories and set permissions
RUN mkdir -p /data/files && \
    chown -R appuser:appuser /app /data/files

# Copy migrate tool from builder stage
COPY --from=builder /go/bin/migrate /usr/local/bin/
COPY --from=builder /app/yanyiwu/ /go/pkg/mod/github.com/yanyiwu/

# Copy the binary from the builder stage
COPY --from=builder /app/config ./config
COPY --from=builder /app/scripts ./scripts
COPY --from=builder /app/migrations ./migrations
COPY --from=builder /app/dataset/samples ./dataset/samples
COPY --from=builder /root/.duckdb /home/appuser/.duckdb
COPY --from=builder /app/WeKnora .
COPY --from=builder /license-bundle/ ./

# Copy and make entrypoint script executable
COPY --from=builder /app/scripts/docker-entrypoint.sh ./scripts/docker-entrypoint.sh

# Make scripts executable
RUN chmod +x ./scripts/*.sh

# Expose ports
EXPOSE 8080


ENTRYPOINT ["./scripts/docker-entrypoint.sh"]
CMD ["./WeKnora"]
