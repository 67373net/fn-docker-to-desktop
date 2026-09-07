#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FNOS_APP_DIR="${ROOT_DIR}/fnos-app"

echo "=== 开始构建 飞牛OS (fnOS) .fpk 原生安装包 ==="

# 1. 确保目录结构
mkdir -p "${FNOS_APP_DIR}/app/ui/images"
mkdir -p "${FNOS_APP_DIR}/cmd"
mkdir -p "${FNOS_APP_DIR}/config"
mkdir -p "${FNOS_APP_DIR}/wizard"

# 2. 编译 Linux amd64 原生二进制
echo "--> 正在编译应用原生二进制..."
if command -v go >/dev/null 2>&1; then
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "${FNOS_APP_DIR}/app/put-port-on-desktop" "${ROOT_DIR}/cmd/server"
else
  docker run --rm \
    -v "${ROOT_DIR}:/build" \
    -w /build \
    golang:alpine \
    sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off go build -trimpath -ldflags='-s -w' -o fnos-app/app/put-port-on-desktop ./cmd/server"
fi

chmod +x "${FNOS_APP_DIR}/app/put-port-on-desktop"
chmod +x "${FNOS_APP_DIR}/cmd/"*

# 3. 检查打包工具 (优先使用 fnpack，若未安装则采用标准 fpk 归档格式)
echo "--> 正在打包 .fpk 安装文件..."
cd "${FNOS_APP_DIR}"

FPK_OUT="${ROOT_DIR}/put-port-on-desktop-x86.fpk"

if command -v fnpack >/dev/null 2>&1; then
  fnpack build
  mv put-port-on-desktop.fpk "${FPK_OUT}"
elif [ -x "/tmp/fnpack" ]; then
  /tmp/fnpack build
  mv put-port-on-desktop.fpk "${FPK_OUT}"
else
  tar -czvf "${FPK_OUT}" manifest ICON.PNG ICON_256.PNG app cmd config wizard
fi

echo "[OK] 构建完成！生成的飞牛OS安装包: ${FPK_OUT}"
ls -lh "${FPK_OUT}"
