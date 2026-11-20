#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
out=${1:?supply an absolute output directory}
mkdir -p "$out"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git -C "$root" log -1 --format=%ct)
sha=$(git -C "$root" rev-parse HEAD)

cd "$root/apps/server-webui"
test -s web/index.html
test ! -e web/sw.js
test ! -e web/manifest.webmanifest
go mod verify
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -mod=readonly -trimpath -buildvcs=false -o "$stage/metacubexd-server" .
  tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner -C "$stage" -cf - metacubexd-server \
    -C "$root" LICENSE docs/server-webui.zh-CN.md docs/server-pilot.zh-CN.md \
    apps/server-webui/units/metacubexd-server.service apps/server-webui/units/metacubexd-validator.service \
    | gzip -n > "$out/metacubexd-server-linux-$arch.tar.gz"
done

jq -n --arg sha "$sha" --arg go "$(go env GOVERSION)" --arg node "$(node --version)" \
  --arg pnpm "$(pnpm --version)" --arg run "${GITHUB_RUN_ID:-local}" \
  --arg attempt "${GITHUB_RUN_ATTEMPT:-local}" --arg lock "$(sha256sum "$root/pnpm-lock.yaml" | cut -d' ' -f1)" \
  '{sha:$sha, go:$go, node:$node, pnpm:$pnpm, run:$run, attempt:$attempt, lockSha256:$lock, platforms:["linux/amd64","linux/arm64"]}' \
  > "$out/build-manifest.json"
cd "$out"
sha256sum build-manifest.json metacubexd-server-linux-{amd64,arm64}.tar.gz > SHA256SUMS
sha256sum --check SHA256SUMS
