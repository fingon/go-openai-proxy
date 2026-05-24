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

| Config | CLI | Default |
| --- | --- | --- |
| Host binding | `--host` | `127.0.0.1` |
| Port | `--port` | `17132` |
| Model allowlist | `--models` | Account-specific Codex models |
| Codex API version | `--codex-version` | Local `codex --version`, registry latest, then `0.111.0` |
| Upstream base URL | `--base-url` | `https://chatgpt.com/backend-api/codex` |
| OAuth client id | `--oauth-client-id` | `app_EMoamEEZ73f0CkXaXp7hrann` |
| OAuth token URL | `--oauth-token-url` | `https://auth.openai.com/oauth/token` |
| Auth file path | `--oauth-file` | `$CHATGPT_LOCAL_HOME/auth.json`, `$CODEX_HOME/auth.json`, `~/.chatgpt-local/auth.json`, `~/.codex/auth.json` |
| Verbose logging | `-v`, `--verbose` | disabled |

## Endpoints

- `GET /health`
- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`
- Other `/v1/*` routes are forwarded to the Codex upstream with OAuth headers when the upstream accepts the same route shape.

This is unofficial software and is not affiliated with, endorsed by, or sponsored by OpenAI.
