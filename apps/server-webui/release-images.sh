#!/usr/bin/env bash
set -euo pipefail

assets=${1:?supply a verified artifact directory}
image="ghcr.io/${GH_REPO}-server"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cp apps/server-webui/Containerfile "$stage/Dockerfile"
cp /etc/ssl/certs/ca-certificates.crt "$stage/"
tar -xzOf "$assets/metacubexd-server-linux-amd64.tar.gz" LICENSE > "$stage/LICENSE"
test -s "$stage/LICENSE"
printf '%s\n' "$TAG" > "$stage/release-tag"
for arch in amd64 arm64; do
  tar -xzOf "$assets/metacubexd-server-linux-$arch.tar.gz" metacubexd-server > "$stage/metacubexd-server-$arch"
  test -s "$stage/metacubexd-server-$arch"
done

labels=(
  --label "org.opencontainers.image.source=https://github.com/$GH_REPO"
  --label "org.opencontainers.image.revision=$SHA"
  --label "org.opencontainers.image.version=$TAG"
)
jq -n '{}' > "$stage/platforms.json"
for arch in amd64 arm64; do
  archive="metacubexd-server-image-linux-$arch.tar.gz"
  docker buildx build --platform "linux/$arch" "${labels[@]}" --provenance=false \
    --tag "$image:$TAG" --output "type=docker,dest=$stage/$arch.tar" "$stage"
  gzip -n -c "$stage/$arch.tar" > "$assets/$archive"
  jq --arg arch "$arch" --arg archive "$archive" \
    --arg archiveSha "$(sha256sum "$assets/$archive" | cut -d' ' -f1)" \
    --arg binarySha "$(sha256sum "$stage/metacubexd-server-$arch" | cut -d' ' -f1)" \
    '.[$arch] = {archive: $archive, archiveSha256: $archiveSha, binarySha256: $binarySha}' \
    "$stage/platforms.json" > "$stage/platforms.next"
  mv "$stage/platforms.next" "$stage/platforms.json"
  rm "$stage/$arch.tar"
done

printf '%s' "$GH_TOKEN" | docker login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
docker buildx build --platform linux/amd64,linux/arm64 "${labels[@]}" --provenance=false \
  --tag "$image:$TAG" --push --metadata-file "$stage/metadata.json" "$stage"
digest=$(jq -er '."containerimage.digest" | select(test("^sha256:[0-9a-f]{64}$"))' "$stage/metadata.json")
jq -n --arg tag "$TAG" --arg sha "$SHA" --arg image "$image@$digest" --slurpfile platforms "$stage/platforms.json" \
  '{schema: 1, tag: $tag, sha: $sha, image: $image, platforms: $platforms[0]}' > "$assets/server-images.json"
