#!/usr/bin/env sh
set -eu

CONTAINER_IMAGE="${CONTAINER_IMAGE:-localhost/go-openai-proxy:local}"
CONTAINER_NAME="${CONTAINER_NAME:-go-openai-proxy-local-test}"
CONTAINER_PORT="${CONTAINER_PORT:-17132}"
CONTAINER_MODELS="${CONTAINER_MODELS:-gpt-5.2}"
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
CONTAINER_SERVICE_PORT=17132

if [ ! -f "$CODEX_HOME/auth.json" ]; then
	echo "missing $CODEX_HOME/auth.json; set CODEX_HOME to a directory containing auth.json" >&2
	exit 1
fi

# shellcheck disable=SC2329
cleanup() {
	podman rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}

trap 'cleanup' EXIT

podman rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

podman run --detach --name "$CONTAINER_NAME" \
	--publish "127.0.0.1:$CONTAINER_PORT:$CONTAINER_SERVICE_PORT" \
	--env CODEX_HOME=/codex \
	--env GO_OPENAI_PROXY_HOST=0.0.0.0 \
	--env GO_OPENAI_PROXY_MODELS="$CONTAINER_MODELS" \
	--env GO_OPENAI_PROXY_PORT="$CONTAINER_SERVICE_PORT" \
	--volume "$CODEX_HOME:/codex" \
	"$CONTAINER_IMAGE"

for _ in 1 2 3 4 5 6 7 8 9 10; do
	if curl -fsS "http://127.0.0.1:$CONTAINER_PORT/health" >/dev/null; then
		curl -fsS "http://127.0.0.1:$CONTAINER_PORT/health"
		exit 0
	fi

	sleep 1
done

podman logs "$CONTAINER_NAME"
exit 1
