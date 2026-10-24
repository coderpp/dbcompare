#!/bin/bash
# Build a clean universal app and a drag-to-install DMG from this checkout.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
RELEASE_STAMP="$(date +%Y%m%d-%H%M%S)"
RELEASE_DIR="$ROOT_DIR/bin/releases/$RELEASE_STAMP"
export RELEASE_DIR
mkdir -p "$RELEASE_DIR/build" "$RELEASE_DIR/staging"

npm run build:check --prefix frontend
go test ./internal/...

# Go's Darwin linker currently requires macOS 13. Match the bundle and C compiler.
for arch in arm64 amd64; do
  echo "Building macOS $arch..."
  GOOS=darwin GOARCH="$arch" CGO_ENABLED=1 \
    MACOSX_DEPLOYMENT_TARGET=13.0 \
    CGO_CFLAGS="-mmacosx-version-min=13.0" \
    CGO_CXXFLAGS="-mmacosx-version-min=13.0" \
    CGO_LDFLAGS="-mmacosx-version-min=13.0" \
    go build -tags production -trimpath -buildvcs=false -ldflags='-w -s' \
    -o "$RELEASE_DIR/build/dbcompare-$arch" . \
    > "$RELEASE_DIR/build/$arch.log" 2>&1
done

APP="$RELEASE_DIR/staging/DB Compare.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create "$RELEASE_DIR/build/dbcompare-arm64" "$RELEASE_DIR/build/dbcompare-amd64" -output "$APP/Contents/MacOS/dbcompare"
cp build/darwin/icons.icns "$APP/Contents/Resources/"
cp THIRD_PARTY_NOTICES.md "$APP/Contents/Resources/"
plutil -convert xml1 -o "$APP/Contents/Info.plist" build/darwin/Info.plist
python3 - <<'PY'
import os, pathlib, plistlib
root = pathlib.Path(os.environ['RELEASE_DIR'])
with (root / 'staging/DB Compare.app/Contents/Info.plist').open('rb') as f:
    info = plistlib.load(f)
info['LSMinimumSystemVersion'] = '13.0'
# Never carry development LSEnvironment or machine-specific settings into a release.
info.pop('LSEnvironment', None)
with (root / 'staging/DB Compare.app/Contents/Info.plist').open('wb') as f:
    plistlib.dump(info, f)
(root / 'version.txt').write_text(info['CFBundleShortVersionString'])
PY
codesign --force --deep --sign - "$APP"
codesign --verify --deep --strict --all-architectures "$APP"
for arch in arm64 x86_64; do
  lipo "$APP/Contents/MacOS/dbcompare" -verify_arch "$arch"
done
# Keep the image content intentionally small: app, Applications shortcut, instructions.
ln -s /Applications "$RELEASE_DIR/staging/Applications"
cat > "$RELEASE_DIR/staging/安装说明.txt" <<'TXT'
DB Compare（macOS 通用版）

系统要求：macOS 13 Ventura 或更高版本，支持 Apple Silicon 和 Intel。
安装：将“DB Compare.app”拖到“Applications（应用程序）”。
更新：先退出旧版，再替换“应用程序”中的旧版应用。

此包为本地临时签名，尚未经过 Apple Developer ID 签名和公证。
首次打开可能被 macOS 拦截，请在系统设置中核对并允许打开可信的应用。
如果看到“已损坏”、签名不符或其他异常，请先核对 SHA256 并联系打包者。

本包不包含任何数据库连接配置、密码、数据库内容或执行历史。
另一台 Mac 安装后需要自行配置连接；数据库需能从该 Mac 访问。
已有应用的连接配置和执行历史单独保存在当前用户目录中，替换应用包不会主动删除。
TXT
VERSION="$(cat "$RELEASE_DIR/version.txt")"
ARTIFACT_NAME="DB-Compare-${VERSION}-$(date +%Y%m%d)-universal"
hdiutil create -volname 'DB Compare' -srcfolder "$RELEASE_DIR/staging" -format UDZO "$RELEASE_DIR/$ARTIFACT_NAME.dmg"
hdiutil verify "$RELEASE_DIR/$ARTIFACT_NAME.dmg"
cp "$RELEASE_DIR/staging/安装说明.txt" "$RELEASE_DIR/安装说明.txt"
(cd "$RELEASE_DIR" && shasum -a 256 "$ARTIFACT_NAME.dmg" > SHA256SUMS.txt)
printf '%s\n' "$RELEASE_DIR" > "$ROOT_DIR/bin/latest-macos-release.txt"
echo "Release: $RELEASE_DIR/$ARTIFACT_NAME.dmg"
