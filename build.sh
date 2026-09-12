#!/bin/sh
# 全OS向けの配布用バイナリを dist/ に作る（Go さえあれば Windows/Mac/Linux どこでも実行可）
#   使い方: ./build.sh [バージョン]
set -e
cd "$(dirname "$0")"
VERSION="${1:-dev}"
rm -rf dist && mkdir -p dist

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  ext=""; [ "$os" = "windows" ] && ext=".exe"
  name="sampledb-$VERSION-$os-$arch"
  mkdir -p "dist/$name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name/sampledb$ext" .
  cp README.md sample_users.csv "dist/$name/"
  # 配布用アーカイブ: Windows は zip、Mac/Linux は実行権限を保持できる tar.gz
  if [ "$os" = "windows" ]; then
    (cd dist && powershell.exe -NoProfile -Command "Compress-Archive -Force -Path '$name' -DestinationPath '$name.zip'" 2>/dev/null \
      || zip -qr "$name.zip" "$name")
  else
    tar -C dist --mode=755 -czf "dist/$name.tar.gz" "$name"
  fi
  echo "built dist/$name"
done
