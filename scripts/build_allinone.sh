#!/usr/bin/env bash
# 构建 WeKnora 单镜像（frontend + app + docreader）。
#
# 用法：
#   ./scripts/build_allinone.sh                     # 本机架构，加载到本地 docker
#   PLATFORMS=linux/amd64,linux/arm64 PUSH=1 ./scripts/build_allinone.sh
#   WITH_ANYDOC=0 ./scripts/build_allinone.sh       # 跳过 Rust anydoc，明显更快
#
# 变量：
#   IMAGE       镜像名（默认 swr.cn-north-4.myhuaweicloud.com/xbh/orion/lolr-kb-allinone）
#   TAG         标签（默认取 VERSION 文件；禁止 latest）
#   PLATFORMS   目标平台，逗号分隔（默认本机架构）
#   PUSH=1      推送到镜像仓库（多平台时必须）
#   VITE_BASE_PATH  前端构建 base（必须与 Orion expose.route 一致，默认 /lolr-kb/）
#   WITH_ANYDOC 1/0（默认 1）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo dev)}"
COMMIT_ID="${COMMIT_ID:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

IMAGE="${IMAGE:-swr.cn-north-4.myhuaweicloud.com/xbh/orion/lolr-kb-allinone}"
TAG="${TAG:-${VERSION}}"
VITE_BASE_PATH="${VITE_BASE_PATH:-/lolr-kb/}"
WITH_ANYDOC="${WITH_ANYDOC:-1}"
PLATFORMS="${PLATFORMS:-}"

if [ "${TAG}" = "latest" ]; then
    echo "error: 禁止 latest 标签（Orion 会拒绝不可变版本校验）" >&2
    exit 1
fi

if [ -z "${PLATFORMS}" ]; then
    case "$(uname -m)" in
        x86_64) PLATFORMS="linux/amd64" ;;
        aarch64|arm64) PLATFORMS="linux/arm64" ;;
        *) echo "error: unsupported host arch $(uname -m); set PLATFORMS explicitly" >&2; exit 1 ;;
    esac
fi

OUTPUT_ARGS=()
if [ "${PUSH:-0}" = "1" ]; then
    OUTPUT_ARGS+=(--push)
elif [[ "${PLATFORMS}" == *,* ]]; then
    echo "error: 多平台构建必须 PUSH=1（或改用 --output=type=oci）" >&2
    exit 1
else
    OUTPUT_ARGS+=(--load)
fi

echo "=== WeKnora all-in-one ==="
echo "  image      : ${IMAGE}:${TAG}"
echo "  platforms  : ${PLATFORMS}"
echo "  base path  : ${VITE_BASE_PATH}"
echo "  anydoc     : ${WITH_ANYDOC}"
echo "  version    : ${VERSION} (${COMMIT_ID})"
echo "  push       : ${PUSH:-0}"
echo ""

docker buildx build \
    --file docker/Dockerfile.allinone \
    --platform "${PLATFORMS}" \
    --build-arg VERSION_ARG="${VERSION}" \
    --build-arg COMMIT_ID_ARG="${COMMIT_ID}" \
    --build-arg BUILD_TIME_ARG="${BUILD_TIME}" \
    --build-arg VITE_FRONTEND_COMMIT="${COMMIT_ID}" \
    --build-arg VITE_BASE_PATH="${VITE_BASE_PATH}" \
    --build-arg WITH_ANYDOC="${WITH_ANYDOC}" \
    --build-arg NPM_REGISTRY="${NPM_REGISTRY:-https://registry.npmmirror.com}" \
    --build-arg GOPROXY_ARG="${GOPROXY_ARG:-https://goproxy.cn,direct}" \
    --build-arg APK_MIRROR_ARG="${APK_MIRROR_ARG:-mirrors.aliyun.com}" \
    --build-arg APT_MIRROR="${APT_MIRROR:-mirrors.aliyun.com}" \
    --build-arg PIP_INDEX_URL="${PIP_INDEX_URL:-https://mirrors.aliyun.com/pypi/simple/}" \
    --build-arg PLAYWRIGHT_DOWNLOAD_HOST="${PLAYWRIGHT_DOWNLOAD_HOST:-https://npmmirror.com/mirrors/playwright}" \
    --build-arg GITHUB_PROXY="${GITHUB_PROXY:-https://gh-proxy.com/}" \
    --build-arg RUSTUP_DIST_SERVER="${RUSTUP_DIST_SERVER:-https://mirrors.ustc.edu.cn/rust-static}" \
    --build-arg RUSTUP_UPDATE_ROOT="${RUSTUP_UPDATE_ROOT:-https://mirrors.ustc.edu.cn/rust-static/rustup}" \
    --build-arg RUSTUP_INSTALLER_URL="${RUSTUP_INSTALLER_URL:-https://sh.rustup.rs}" \
    --build-arg CARGO_REGISTRY_MIRROR="${CARGO_REGISTRY_MIRROR:-sparse+https://rsproxy.cn/index/}" \
    --tag "${IMAGE}:${TAG}" \
    "${OUTPUT_ARGS[@]}" \
    .
