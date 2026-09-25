#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FNOS_APP_DIR="${ROOT_DIR}/fnos-app"
APP_NAME="fn-docker-to-desktop"
ARCH="${1:-x86}"

echo "=== 开始构建 飞牛OS (fnOS) .fpk 原生安装包 [${ARCH}] ==="

# 1. 确保目录结构并同步根目录权威高清产品图标
mkdir -p "${FNOS_APP_DIR}/app/ui/images"
mkdir -p "${FNOS_APP_DIR}/cmd"
mkdir -p "${FNOS_APP_DIR}/config"
mkdir -p "${FNOS_APP_DIR}/wizard"

if [ -f "${ROOT_DIR}/icon.png" ]; then
  echo "--> 正在同步并生成各尺寸官方规范图标至 web/ 与 fnos-app/ (64x64, 256x256)..."
  if command -v python3 >/dev/null 2>&1 && python3 -c "import PIL" 2>/dev/null; then
    python3 -c "from PIL import Image
src = Image.open('${ROOT_DIR}/icon.png').convert('RGBA')
img64 = src.resize((64, 64), Image.Resampling.LANCZOS)
img256 = src.resize((256, 256), Image.Resampling.LANCZOS)
img64.save('${FNOS_APP_DIR}/ICON.PNG', 'PNG')
img256.save('${FNOS_APP_DIR}/ICON_256.PNG', 'PNG')
img64.save('${FNOS_APP_DIR}/app/ui/images/icon-64.png', 'PNG')
img256.save('${FNOS_APP_DIR}/app/ui/images/icon-256.png', 'PNG')
img64.save('${ROOT_DIR}/internal/desktop/assets/PRODUCT_ICON.PNG', 'PNG')
img256.save('${ROOT_DIR}/internal/desktop/assets/PRODUCT_ICON_256.PNG', 'PNG')
img256.save('${ROOT_DIR}/web/icon.png', 'PNG')
"
  else
    cp "${ROOT_DIR}/icon.png" "${ROOT_DIR}/web/icon.png"
    cp "${ROOT_DIR}/icon.png" "${FNOS_APP_DIR}/ICON.PNG"
    cp "${ROOT_DIR}/icon.png" "${FNOS_APP_DIR}/ICON_256.PNG"
    cp "${ROOT_DIR}/icon.png" "${FNOS_APP_DIR}/app/ui/images/icon-64.png"
    cp "${ROOT_DIR}/icon.png" "${FNOS_APP_DIR}/app/ui/images/icon-256.png"
  fi
  # 清理任何非规范图标文件
  rm -f "${FNOS_APP_DIR}/app/ui/images/icon-{0}.png" "${FNOS_APP_DIR}/app/ui/images/icon.png" 2>/dev/null || true
fi

# 2. 编译 Linux 原生二进制
echo "--> 正在编译应用原生二进制 (${ARCH})..."
GOARCH="amd64"
if [ "${ARCH}" = "arm" ] || [ "${ARCH}" = "arm64" ]; then
  GOARCH="arm64"
fi

BIN_PATH="${FNOS_APP_DIR}/app/${APP_NAME}"
if command -v go >/dev/null 2>&1; then
  CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} go build -trimpath -ldflags "-s -w" -o "${BIN_PATH}" "${ROOT_DIR}/cmd/server"
else
  mkdir -p /tmp/gocache
  mkdir -p /tmp/gopath
  docker run --rm \
    -v "${ROOT_DIR}:/build" \
    -v "/tmp/gocache:/root/.cache/go-build" \
    -v "/tmp/gopath:/go" \
    -w /build \
    golang:alpine \
    sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} go build -trimpath -ldflags='-s -w' -o fnos-app/app/${APP_NAME} ./cmd/server && chmod +x fnos-app/app/${APP_NAME}"
fi

chmod +x "${BIN_PATH}" 2>/dev/null || true
chmod +x "${FNOS_APP_DIR}/cmd/"* 2>/dev/null || true

# 3. 打包 .fpk 文件 (严格遵循飞牛OS官方规范: app.tgz + md5 checksum 机制)
echo "--> 正在打包 .fpk 安装文件..."
FPK_OUT="${ROOT_DIR}/${APP_NAME}-${ARCH}.fpk"

if command -v fnpack >/dev/null 2>&1; then
  cd "${FNOS_APP_DIR}"
  fnpack build
  mv "${APP_NAME}.fpk" "${FPK_OUT}"
elif [ -x "/tmp/fnpack" ]; then
  cd "${FNOS_APP_DIR}"
  /tmp/fnpack build
  mv "${APP_NAME}.fpk" "${FPK_OUT}"
else
  echo "--> 正在构建符合飞牛官方规范的 app.tgz 与校验和..."
  TMP_BUILD_DIR=$(mktemp -d)
  trap 'rm -rf "${TMP_BUILD_DIR}"' EXIT

  # 准备 app 内部归档
  mkdir -p "${TMP_BUILD_DIR}/app_staging"
  cp -r "${FNOS_APP_DIR}/app/ui" "${TMP_BUILD_DIR}/app_staging/"
  cp -r "${FNOS_APP_DIR}/config" "${TMP_BUILD_DIR}/app_staging/"
  cp "${BIN_PATH}" "${TMP_BUILD_DIR}/app_staging/${APP_NAME}"
  cp "${FNOS_APP_DIR}/ICON.PNG" "${TMP_BUILD_DIR}/app_staging/"
  cp "${FNOS_APP_DIR}/ICON_256.PNG" "${TMP_BUILD_DIR}/app_staging/"

  # 制作 app.tgz
  tar -czf "${TMP_BUILD_DIR}/app.tgz" -C "${TMP_BUILD_DIR}/app_staging" ui config ICON.PNG ICON_256.PNG "${APP_NAME}"

  # 计算 app.tgz 的 MD5 校验和
  CHECKSUM=$(md5sum "${TMP_BUILD_DIR}/app.tgz" | awk '{print $1}')
  echo "--> app.tgz 校验和: ${CHECKSUM}"

  # 准备 manifest 并追加 checksum
  grep -v "^checksum" "${FNOS_APP_DIR}/manifest" > "${TMP_BUILD_DIR}/manifest"
  echo "checksum              = ${CHECKSUM}" >> "${TMP_BUILD_DIR}/manifest"

  # 复制外层必要目录与图标
  cp -r "${FNOS_APP_DIR}/cmd" "${TMP_BUILD_DIR}/cmd"
  cp -r "${FNOS_APP_DIR}/config" "${TMP_BUILD_DIR}/config"
  cp -r "${FNOS_APP_DIR}/wizard" "${TMP_BUILD_DIR}/wizard"
  cp "${FNOS_APP_DIR}/ICON.PNG" "${TMP_BUILD_DIR}/ICON.PNG"
  cp "${FNOS_APP_DIR}/ICON_256.PNG" "${TMP_BUILD_DIR}/ICON_256.PNG"

  # 打包为标准 .fpk
  (cd "${TMP_BUILD_DIR}" && tar -czf "${FPK_OUT}" app.tgz cmd config ICON.PNG ICON_256.PNG manifest wizard)
fi

echo "[OK] 构建完成！生成的飞牛OS安装包: ${FPK_OUT}"
ls -lh "${FPK_OUT}"
