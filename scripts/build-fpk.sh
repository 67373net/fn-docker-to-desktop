#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
FNOS_APP_DIR="${ROOT_DIR}/fnos-app"
APP_NAME="fn-docker-to-desktop"
ARCH="${1:-all}"
VERSION="${2}"
if [ -z "${VERSION}" ]; then
  VERSION=$(grep "^version" "${FNOS_APP_DIR}/manifest" | awk -F '=' '{print $2}' | tr -d '[:space:]')
fi
if [ -z "${VERSION}" ]; then
  VERSION="1.0.0"
fi

echo "=== 开始构建 飞牛OS (fnOS) .fpk 原生安装包 [${ARCH}] (版本: ${VERSION}) ==="

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
compile_binary() {
  local target_arch="$1"
  local output_bin="$2"
  local bin_name
  bin_name="$(basename "${output_bin}")"
  echo "--> 正在编译应用原生二进制 (${target_arch} -> ${output_bin})..."
  if command -v go >/dev/null 2>&1; then
    CGO_ENABLED=0 GOOS=linux GOARCH=${target_arch} go build -trimpath -ldflags "-s -w" -o "${output_bin}" "${ROOT_DIR}/cmd/server"
  else
    mkdir -p /tmp/gocache
    mkdir -p /tmp/gopath
    docker run --rm \
      -e GOPROXY="https://goproxy.cn,direct" \
      -v "${ROOT_DIR}:/build" \
      -v "/tmp/gocache:/root/.cache/go-build" \
      -v "/tmp/gopath:/go" \
      -w /build \
      golang:alpine \
      sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=${target_arch} go build -trimpath -ldflags='-s -w' -o fnos-app/app/${bin_name} ./cmd/server && chmod +x fnos-app/app/${bin_name} 2>/dev/null || true"
  fi
  chmod +x "${output_bin}" 2>/dev/null || true
}

ARCH_MODE="single"
ARCH_TAG="${ARCH}"
if [ "${ARCH}" = "all" ] || [ "${ARCH}" = "universal" ]; then
  ARCH_MODE="all"
  ARCH_TAG="all"
  compile_binary "amd64" "${FNOS_APP_DIR}/app/${APP_NAME}-amd64"
  compile_binary "arm64" "${FNOS_APP_DIR}/app/${APP_NAME}-arm64"

  # 生成自适应多架构启动入口脚本
  rm -f "${FNOS_APP_DIR}/app/${APP_NAME}" 2>/dev/null || true
  cat <<'EOF' > "${FNOS_APP_DIR}/app/${APP_NAME}"
#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH_M="$(uname -m)"
case "${ARCH_M}" in
  aarch64*|arm64*|armv8*)
    if [ -x "${DIR}/fn-docker-to-desktop-arm64" ]; then
      exec "${DIR}/fn-docker-to-desktop-arm64" "$@"
    fi
    ;;
  *)
    if [ -x "${DIR}/fn-docker-to-desktop-amd64" ]; then
      exec "${DIR}/fn-docker-to-desktop-amd64" "$@"
    fi
    ;;
esac
for bin in "${DIR}/fn-docker-to-desktop"-*; do
  if [ -x "${bin}" ]; then
    exec "${bin}" "$@"
  fi
done
echo "fn-docker-to-desktop: no compatible binary found for ${ARCH_M}" >&2
exit 1
EOF
  chmod +x "${FNOS_APP_DIR}/app/${APP_NAME}"
else
  GOARCH="amd64"
  if [ "${ARCH}" = "arm" ] || [ "${ARCH}" = "arm64" ]; then
    GOARCH="arm64"
    ARCH_TAG="arm"
  else
    ARCH_TAG="x86"
  fi
  compile_binary "${GOARCH}" "${FNOS_APP_DIR}/app/${APP_NAME}"
fi

chmod +x "${FNOS_APP_DIR}/cmd/"* 2>/dev/null || true

# 3. 打包 .fpk 文件 (严格遵循飞牛OS官方规范: app.tgz + md5 checksum 机制)
echo "--> 正在打包 .fpk 安装文件..."
FPK_VERSIONED="${ROOT_DIR}/${APP_NAME}_${VERSION}_${ARCH_TAG}.fpk"
FPK_ALIAS="${ROOT_DIR}/${APP_NAME}-${ARCH_TAG}.fpk"

if command -v fnpack >/dev/null 2>&1; then
  cd "${FNOS_APP_DIR}"
  fnpack build
  mv "${APP_NAME}.fpk" "${FPK_VERSIONED}"
  cp -f "${FPK_VERSIONED}" "${FPK_ALIAS}"
elif [ -x "/tmp/fnpack" ]; then
  cd "${FNOS_APP_DIR}"
  /tmp/fnpack build
  mv "${APP_NAME}.fpk" "${FPK_VERSIONED}"
  cp -f "${FPK_VERSIONED}" "${FPK_ALIAS}"
else
  echo "--> 正在构建符合飞牛官方规范的 app.tgz 与校验和..."
  TMP_BUILD_DIR=$(mktemp -d)
  trap 'rm -rf "${TMP_BUILD_DIR}"' EXIT

  # 准备 app 内部归档
  mkdir -p "${TMP_BUILD_DIR}/app_staging"
  cp -r "${FNOS_APP_DIR}/app/ui" "${TMP_BUILD_DIR}/app_staging/"
  cp -r "${FNOS_APP_DIR}/config" "${TMP_BUILD_DIR}/app_staging/"
  cp "${FNOS_APP_DIR}/app/${APP_NAME}" "${TMP_BUILD_DIR}/app_staging/${APP_NAME}"
  if [ "${ARCH_MODE}" = "all" ]; then
    cp "${FNOS_APP_DIR}/app/${APP_NAME}-amd64" "${TMP_BUILD_DIR}/app_staging/"
    cp "${FNOS_APP_DIR}/app/${APP_NAME}-arm64" "${TMP_BUILD_DIR}/app_staging/"
  fi
  cp "${FNOS_APP_DIR}/ICON.PNG" "${TMP_BUILD_DIR}/app_staging/"
  cp "${FNOS_APP_DIR}/ICON_256.PNG" "${TMP_BUILD_DIR}/app_staging/"

  # 制作 app.tgz
  if [ "${ARCH_MODE}" = "all" ]; then
    tar -czf "${TMP_BUILD_DIR}/app.tgz" -C "${TMP_BUILD_DIR}/app_staging" ui config ICON.PNG ICON_256.PNG "${APP_NAME}" "${APP_NAME}-amd64" "${APP_NAME}-arm64"
  else
    tar -czf "${TMP_BUILD_DIR}/app.tgz" -C "${TMP_BUILD_DIR}/app_staging" ui config ICON.PNG ICON_256.PNG "${APP_NAME}"
  fi

  # 计算 app.tgz 的 MD5 校验和
  CHECKSUM=$(md5sum "${TMP_BUILD_DIR}/app.tgz" | awk '{print $1}')
  echo "--> app.tgz 校验和: ${CHECKSUM}"

  # 准备 manifest 并配置对应平台
  grep -v "^checksum" "${FNOS_APP_DIR}/manifest" > "${TMP_BUILD_DIR}/manifest"
  if [ "${ARCH_TAG}" = "all" ]; then
    sed -i '/^arch.*=.*/d' "${TMP_BUILD_DIR}/manifest"
    sed -i 's/^platform.*=.*/platform        = all/' "${TMP_BUILD_DIR}/manifest"
  elif [ "${ARCH_TAG}" = "arm" ]; then
    sed -i '/^arch.*=.*/d' "${TMP_BUILD_DIR}/manifest"
    sed -i 's/^platform.*=.*/platform        = arm/' "${TMP_BUILD_DIR}/manifest"
  else
    sed -i 's/^platform.*=.*/platform        = x86/' "${TMP_BUILD_DIR}/manifest"
    if ! grep -q "^arch" "${TMP_BUILD_DIR}/manifest"; then
      echo "arch            = x86_64" >> "${TMP_BUILD_DIR}/manifest"
    fi
  fi
  echo "checksum              = ${CHECKSUM}" >> "${TMP_BUILD_DIR}/manifest"

  # 复制外层必要目录与图标
  cp -r "${FNOS_APP_DIR}/cmd" "${TMP_BUILD_DIR}/cmd"
  cp -r "${FNOS_APP_DIR}/config" "${TMP_BUILD_DIR}/config"
  cp -r "${FNOS_APP_DIR}/wizard" "${TMP_BUILD_DIR}/wizard"
  cp "${FNOS_APP_DIR}/ICON.PNG" "${TMP_BUILD_DIR}/ICON.PNG"
  cp "${FNOS_APP_DIR}/ICON_256.PNG" "${TMP_BUILD_DIR}/ICON_256.PNG"

  # 打包为标准 .fpk
  (cd "${TMP_BUILD_DIR}" && tar -czf "${FPK_VERSIONED}" app.tgz cmd config ICON.PNG ICON_256.PNG manifest wizard)
  cp -f "${FPK_VERSIONED}" "${FPK_ALIAS}"
fi

echo "[OK] 构建完成！生成的飞牛OS安装包: ${FPK_VERSIONED} 与别名 ${FPK_ALIAS}"
ls -lh "${FPK_VERSIONED}" "${FPK_ALIAS}"
