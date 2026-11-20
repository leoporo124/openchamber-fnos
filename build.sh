#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
printf '%s\n' '[INFO] 构建 OpenChamber fnOS x86_64 安装包'
(
    cd frontend
    npm ci --ignore-scripts --no-audit --no-fund
    ./node_modules/.bin/vue-tsc --noEmit
    npm run build
)
mkdir -p fnpack/app/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o fnpack/app/bin/openchamber .
chmod 755 fnpack/app/bin/openchamber fnpack/cmd/*
(
    cd fnpack
    chmod +x fnpack-1.2.3-linux-amd64
    ./fnpack-1.2.3-linux-amd64 build
)
printf '%s\n' '[INFO] 已生成 fnpack/openchamber.fpk，运行行为需在 fnOS 手动验收。'
