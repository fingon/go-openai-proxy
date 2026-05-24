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
| Codex API version | `--codex-version` | `GO_OPENAI_PROXY_CODEX_VERSION` | Registry latest, then `0.111.0` |
| Upstream base URL | `--base-url` | `GO_OPENAI_PROXY_BASE_URL` | `https://chatgpt.com/backend-api/codex` |
| OAuth client id | `--oauth-client-id` | `GO_OPENAI_PROXY_OAUTH_CLIENT_ID` | `app_EMoamEEZ73f0CkXaXp7hrann` |
| OAuth token URL | `--oauth-token-url` | `GO_OPENAI_PROXY_OAUTH_TOKEN_URL` | `https://auth.openai.com/oauth/token` |
| Auth file path | `--oauth-file` | `GO_OPENAI_PROXY_OAUTH_FILE` | `$CHATGPT_LOCAL_HOME/auth.json`, `$CODEX_HOME/auth.json`, `~/.chatgpt-local/auth.json`, `~/.codex/auth.json` |
| Verbose logging | `-v`, `--verbose` | `GO_OPENAI_PROXY_VERBOSE` | disabled |

## Container

The container is built with `ko` and still expects a mounted Codex auth directory. Tokens are not supplied directly through environment variables.

```bash
podman run --rm \
  --publish 127.0.0.1:17132:17132 \
  --env CODEX_HOME=/codex \
  --env GO_OPENAI_PROXY_HOST=0.0.0.0 \
  --volume "$HOME/.codex:/codex" \
  ghcr.io/fingon/go-openai-proxy:latest
```

For local smoke testing, install `ko`, `podman`, and `curl`, make sure the Podman machine or socket is running, and make sure `$HOME/.codex/auth.json` exists. If your auth directory is elsewhere, set `CODEX_HOME` for the Make target.

```bash
rtk make container-test-local
```

The local target builds the image with `ko`, loads it into Podman, runs it with the Codex directory mounted, checks `/health`, and removes the test container. It sets `GO_OPENAI_PROXY_MODELS=gpt-5.2` by default so the smoke test does not need to call the upstream model catalog; override `CONTAINER_MODELS` to test another model list.

## Endpoints

- `GET /health`
- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`
- Other `/v1/*` routes are forwarded to the Codex upstream with OAuth headers when the upstream accepts the same route shape.

This is unofficial software and is not affiliated with, endorsed by, or sponsored by OpenAI.
