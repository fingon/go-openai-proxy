# go-openai-proxy

OpenAI-compatible local endpoint backed by the local ChatGPT/Codex OAuth cache.

## Usage

```bash
go run ./cmd/go-openai-proxy
```

When startup succeeds, the CLI prints the local `/v1` base URL and available models.

If no auth file is available, run:

```bash
codex login
```

## Configuration

| Config | CLI | Environment | Default |
| --- | --- | --- | --- |
| Host binding | `--host` | `GO_OPENAI_PROXY_HOST` | `127.0.0.1` |
| Port | `--port` | `GO_OPENAI_PROXY_PORT` | `17132` |
| Model allowlist | `--models` | `GO_OPENAI_PROXY_MODELS` | Account-specific Codex models |
| Codex API version | `--codex-version` | `GO_OPENAI_PROXY_CODEX_VERSION` | Installed `codex --version`, then registry latest |
| Upstream base URL | `--base-url` | `GO_OPENAI_PROXY_BASE_URL` | `https://chatgpt.com/backend-api/codex` |
| OAuth client id | `--oauth-client-id` | `GO_OPENAI_PROXY_OAUTH_CLIENT_ID` | `app_EMoamEEZ73f0CkXaXp7hrann` |
| OAuth token URL | `--oauth-token-url` | `GO_OPENAI_PROXY_OAUTH_TOKEN_URL` | `https://auth.openai.com/oauth/token` |
| Auth file path | `--oauth-file` | `GO_OPENAI_PROXY_OAUTH_FILE` | `$CHATGPT_LOCAL_HOME/auth.json`, `$CODEX_HOME/auth.json`, `~/.chatgpt-local/auth.json`, `~/.codex/auth.json` |
| Disable OAuth refresh | `--no-refresh` | `GO_OPENAI_PROXY_NO_REFRESH` | disabled |
| Verbose logging | `-v`, `--verbose` | `GO_OPENAI_PROXY_VERBOSE` | disabled |

## Container

The container is built with `ko` and still expects a mounted Codex auth directory. Tokens are not supplied directly through environment variables.

To provision `auth.json` without installing Codex locally, use Podman to run the Codex CLI against a mounted `.codex` directory:

```bash
mkdir -p "codex"
podman run --rm -it \
  --userns keep-id \
  --env CODEX_HOME=/codex \
  --env HOME=/tmp \
  --env npm_config_cache=/tmp/.npm \
  --volume "$PWD/codex:/codex" \
  docker.io/library/node:lts-bookworm \
  sh -lc 'printf "%s\n" "cli_auth_credentials_store = \"file\"" > /codex/config.toml && npm exec -y --package @openai/codex -- codex login --device-auth'
test -f "codex/auth.json"
```

Treat `auth.json` as secret material. Do not commit it, paste it into logs, or share one writable auth file across multiple running proxy instances. The proxy refreshes OAuth tokens when needed and writes the updated token state back to `auth.json`, so mount the directory read-write. Use `--no-refresh` if another process owns OAuth refresh; the proxy will still reload same-account `auth.json` after a 401, but it will not call the OAuth refresh endpoint.

The image runs as container UID/GID `0:0` by default. On rootless Linux Podman, container root maps back to the rootless host user, which allows the proxy to refresh a bind-mounted `auth.json` owned by your host user with mode `0600`.

```bash
podman run --rm \
  --publish 127.0.0.1:17132:17132 \
  --env CODEX_HOME=/codex \
  --env GO_OPENAI_PROXY_HOST=0.0.0.0 \
  --volume "$PWD/codex:/codex" \
  ghcr.io/fingon/go-openai-proxy:latest
```

On SELinux-enforcing hosts, add a private label to the bind mount, for example `--volume "$PWD/codex:/codex:Z"`.

For local smoke testing, install `ko`, `podman`, and `curl`, make sure the Podman machine or socket is running, and make sure `$HOME/.codex/auth.json` exists. If your auth directory is elsewhere, set `CODEX_HOME` for the Make target.

```bash
rtk make container-test-local
```

The local target builds the image with `ko`, loads it into Podman, runs it with the Codex directory mounted, checks `/health`, and removes the test container. It sets `GO_OPENAI_PROXY_MODELS=gpt-5.2` by default so the smoke test does not need to call the upstream model catalog; override `CONTAINER_MODELS` to test another model list.

## Endpoints

The proxy intentionally supports only routes that have been tested with Codex OAuth authentication. Unsupported `/v1/*` routes return a JSON `404` and are not forwarded to the Codex upstream.

| Endpoint | Status | Notes |
| --- | --- | --- |
| `GET /health` | Supported | Local health check. |
| `GET /v1/models` | Supported | Returns the Codex model catalog or the configured model allowlist. |
| `GET /v1/models/{model}` | Supported | Local lookup against the same model list as `GET /v1/models`. |
| `POST /v1/responses` | Supported | Stateless. Streaming and non-streaming calls are tested. |
| `POST /v1/chat/completions` | Supported | Compatibility adapter backed by Codex Responses. Streaming and non-streaming calls are tested. |
| `POST /v1/embeddings` | Unsupported | Tested with Codex OAuth and not accepted by the Codex upstream route shape. |
| `POST /v1/completions` | Unsupported | Tested with Codex OAuth and not accepted by the Codex upstream route shape. |

The Responses adapter always sends a streaming request to Codex and aggregates the SSE events for non-streaming callers. It sets `store=false` by default and rejects `previous_response_id` and `item_reference`, so clients must replay the full conversation history in `input` on each request. Response retrieval, input item listing, and other server-side replay state APIs are not supported.

To run the live endpoint smoke test against your Codex auth cache:

```bash
CODEX_HOME="$PWD/codex" rtk make test-openai-endpoints
```

The live test uses your existing `auth.json` in place with OAuth refresh disabled, then verifies models, Responses, Chat Completions, and the unsupported Embeddings route. It does not print token material.

This is unofficial software and is not affiliated with, endorsed by, or sponsored by OpenAI.
