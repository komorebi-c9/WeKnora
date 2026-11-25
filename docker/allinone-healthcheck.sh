#!/bin/sh
# WeKnora 单镜像健康检查：nginx 入口 + app /health + docreader gRPC。
set -eu

# 与 allinone-entrypoint.sh 保持同一默认值：nginx 监听端口（默认 15173）。
NGINX_LISTEN_PORT="${NGINX_LISTEN_PORT:-15173}"

curl -fsS --max-time 5 "http://127.0.0.1:${NGINX_LISTEN_PORT}/health" >/dev/null
curl -fsS --max-time 5 http://127.0.0.1:8080/health >/dev/null
grpc_health_probe -addr=127.0.0.1:50051 -rpc-timeout=5s >/dev/null
