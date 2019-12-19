#!/bin/sh

# Build
CGO_ENABLED=0 GOOS=linux go build -v -trimpath -ldflags "-s" -a

# Compress
upx flunix || echo 'Not using upx'

# Package
VERSION="$(grep '* Version:' README.md | cut -d' ' -f3)"
mkdir -p "flunix-$VERSION"
mv -v flunix "flunix-$VERSION"
tar zcvf "flunix-$VERSION-static_linux.tar.gz" "flunix-$VERSION"
rm -rf "flunix-$VERSION"

# Size
du -h "flunix-$VERSION-static_linux.tar.gz"
