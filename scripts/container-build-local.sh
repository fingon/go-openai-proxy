#!/usr/bin/env sh
set -eu

CONTAINER_IMAGE_REPO="${CONTAINER_IMAGE_REPO:-localhost/go-openai-proxy}"
CONTAINER_IMAGE_TAG="${CONTAINER_IMAGE_TAG:-local}"

tarball_base="$(mktemp /tmp/go-openai-proxy-ko.XXXXXX)"
tarball="${tarball_base}.tar"

# shellcheck disable=SC2329
cleanup() {
	rm -f "$tarball_base" "$tarball"
}

trap 'cleanup' EXIT

rm -f "$tarball_base"

KO_DOCKER_REPO="$CONTAINER_IMAGE_REPO" ko build \
	--bare \
	--push=false \
	--tags "$CONTAINER_IMAGE_TAG" \
	--tarball "$tarball" \
	./cmd/go-openai-proxy

podman load --input "$tarball"
