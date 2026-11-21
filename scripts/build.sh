#!/bin/sh
#
#   Builds the addon packages: dist/notify-<arch>-<version>.tar.gz and .sha256 for aarch64, x86_64
#   and armv7l (the asset names openccu-lite's catalogue resolves by uname -m).
#
#     VERSION=0.1.0 scripts/build.sh            # all three
#     ARCHES=x86_64 scripts/build.sh            # one
#     SKIP_UI=1 scripts/build.sh                # keep addon/notify/app as it is
#
#   The page is built with npm in ui/ and copied to addon/notify/app (the daemon serves it at
#   /addons/notify/); the manifest's icons, the 503 page and the proxy rule live in addon/notify/www
#   and addon/notify/etc, tracked, where the catalogue reads the icons from the repository at the
#   release tag; the daemon is a static Go binary (CGO_ENABLED=0) per architecture.
#
set -e
cd "$(dirname "$0")/.."
VERSION=${VERSION:-0.0.0-dev}
ARCHES=${ARCHES:-aarch64 x86_64 armv7l}
GO=${GO:-go}

if [ -z "$SKIP_UI" ]; then
    (cd ui && npm ci --no-audit --no-fund && npm run build)
    rm -rf addon/notify/app
    mkdir -p addon/notify/app
    cp -a ui/dist/. addon/notify/app/
fi
cp -a addon/static/. addon/notify/www/
printf 'VERSION_ADDON=%s\n' "$VERSION" > addon/notify/versions

mkdir -p dist
for arch in $ARCHES; do
    case $arch in
        aarch64) goarch=arm64; goarm= ;;
        x86_64) goarch=amd64; goarm= ;;
        armv7l) goarch=arm; goarm=7 ;;
        *) echo "unknown architecture $arch" >&2; exit 1 ;;
    esac
    mkdir -p addon/notify/bin
    rm -f addon/notify/bin/notify
    CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm $GO build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o addon/notify/bin/notify ./cmd/notify

    out=dist/notify-$arch-$VERSION.tar.gz
    stage=$(mktemp -d)
    cp -a addon/update_script addon/openccu-lite.json addon/notify "$stage/"
    rm -rf "$stage/notify/var"
    find "$stage" \( -name '._*' -o -name '.DS_Store' \) -exec rm -f {} +
    chmod 755 "$stage/update_script" "$stage/notify/rc.d" "$stage/notify/bin/notify"
    test -f "$stage/notify/etc/lighttpd.conf"
    COPYFILE_DISABLE=1 tar --owner=root --group=root --sort=name --mtime='2026-01-01 00:00Z' -czf "$out" -C "$stage" .
    rm -rf "$stage"
    (cd dist && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")
    echo "built $out"
done
