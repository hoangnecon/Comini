# Comini v2.0 — Native Google Antigravity Proxy & CLI for Codex Desktop

Comini is a zero-dependency, native Go proxy bridge and CLI manager that connects **Codex Desktop (Responses API 2026)** directly to **Google Cloud Code Antigravity (CCA)** models (`gemini-3.8-flash`, `gemini-4-argon`, `claude-sonnet-5-5`, `claude-opus-5-5-thinking`, and `gpt-oss-120b-medium`).

## Key Features

- **Native Google Antigravity OAuth2 & Account Manager**: Built-in browser OAuth2 authentication (`comini auth login`), multi-account switching (`comini auth switch`), and atomic token refresh persistence (`~/.codex/comini_auth.json` with `0600` permissions).
- **Real-Time Quota Inspector**: Query live Google CCA model quotas and reset windows via CLI (`comini quota`) or HTTP (`GET /v1/quota`).
- **macOS LaunchAgent Daemon Manager**: Install, start, stop, restart, and inspect background proxy service logs natively (`comini service install`).
- **Responses API 2026 Full Lifecycle**: Complete SSE streaming parity (`response.created`, `reasoning_summary_text`, `function_call_arguments`, `custom_tool_call_input`, `image_generation_call`, `tool_search_call`, `response.completed`).
- **1:1 3-Rung Effort Routing**: Direct mapping of Codex reasoning effort (`low`, `medium`, `high`) to Google CCA wire models (`gemini-3.8-flash-{low,medium,high}`, `gemini-4-argon-{low,medium,high}`, `claude-sonnet-5-5-{low,medium,high}`, `claude-opus-5-5-{low,medium,high}`).
- **Quantized Prefix-Cache Engine**: Enforces a 200k-token (`800,000` chars) context ceiling and a 10k-token (`48,000` chars) tool output cap while quantizing historical pruning and tool output decay to 16-turn epoch boundaries (`cutoffIndex := (rawCutoff / 16) * 16`) for 95%+ prefix cache hit rates.
- **Thought Signature Store**: Persistent tiered signature store (`~/.codex/comini_signatures.json`) ensuring cryptographic continuity on historical `functionCall` turns.

---

## CLI Usage

```bash
# Build and install globally (/opt/homebrew/bin/comini) + macOS LaunchAgent
go build -o comini ./cmd/server
./comini service install

# Check service, proxy health, and authentication status
comini status

# Authenticate or add another Google Antigravity account via browser OAuth2
comini auth login

# Inspect stored accounts, switch active account, or force token refresh
comini auth status
comini auth switch <email|account_id>
comini auth refresh
comini auth logout [email|all]

# Inspect real-time model quotas and reset timers from Google CCA
comini quota

# Manage background LaunchAgent service
comini service status
comini service restart
comini service logs
comini service stop
```

---

## HTTP Endpoints (`127.0.0.1:9876`)

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `/v1/responses` | `POST` | OpenAI Responses API 2026 SSE streaming endpoint for Codex Desktop |
| `/v1/models` | `GET` | Active model catalog with 3-rung reasoning ladder and context window metadata |
| `/v1/quota` | `GET` | Real-time Google Cloud Code Antigravity quota and reset timestamps |
| `/v1/auth/status` | `GET` | Current OAuth2 authentication status and redacted account store |
| `/health` | `GET` | Service readiness, active account email, and upstream configuration |

---

## Active Model Routing

| Client Model ID | Effort Rung | Upstream CCA Wire Model | Context Window |
| :--- | :--- | :--- | :--- |
| `google-antigravity/gemini-3.8-flash` | `low` / `medium` / `high` | `gemini-3.8-flash-{low,medium,high}` | `272,000` |
| `google-antigravity/gemini-4-argon` | `low` / `medium` / `high` | `gemini-4-argon-{low,medium,high}` | `272,000` |
| `google-antigravity/claude-sonnet-5-5` | `low` / `medium` / `high` | `claude-sonnet-5-5-{low,medium,high}` | `250,000` |
| `google-antigravity/claude-opus-5-5-thinking` | `low` / `medium` / `high` | `claude-opus-5-5-{low,medium,high}` | `250,000` |
| `google-antigravity/gpt-oss-120b-medium` | `medium` | `gpt-oss-120b-medium` | `131,072` |

---

## Project Structure

- `cmd/server/main.go`: Unified CLI (`serve`, `auth`, `quota`, `status`, `service`) and HTTP server entrypoint.
- `internal/config/config.go`: Environment and runtime configuration (`PORT`, `HOST`, `CCA_BASE_URL`, `COMINI_AUTH_FILE`).
- `internal/domain/`: Wire types for OpenAI Responses API 2026 and Google CCA SSE envelopes.
- `internal/harness/sanitizer.go`: Prompt identity neutralization and active tool catalog contract builder.
- `internal/schema/compiler.go`: OpenAPI / JSON Schema to Gemini `FunctionDeclaration` compiler.
- `internal/server/`: HTTP router and handlers (`/v1/responses`, `/v1/models`, `/v1/quota`, `/v1/auth/status`, `/health`).
- `internal/session/`: Quantized context budgeter (`turn_builder.go`) and persistent `SignatureStore` (`store.go`).
- `internal/streaming/mapper.go`: Real-time state machine mapping Gemini SSE chunks to Responses API 2026 events.
- `internal/transport/`: OAuth2 & multi-account manager (`auth.go`), resilient CCA HTTP/2 client (`cca_client.go`), and model/effort router (`router.go`).
- `tests/`: Automated unit, schema, streaming, and live CCA integration tests.

## Running Tests

```bash
go test -v ./tests/...
```

See [RULES.md](RULES.md) for strict architectural invariants and engineering standards.
