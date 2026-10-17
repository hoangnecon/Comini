# Comini: System Rules, Invariants & Engineering Standards

## 1. Project Mission & Architecture
Comini is a zero-dependency native Go proxy and CLI management tool that connects **Codex Desktop (Responses API 2026)** to **Google Cloud Code Antigravity (CCA)**.

Comini provides full protocol parity and standalone lifecycle management:
- **Standalone OAuth2 & Account Management**: Browser-based OAuth2 login, multi-account switching, and atomic token persistence in `~/.codex/comini_auth.json` (`0600`).
- **Responses API 2026 SSE Streaming Protocol**: Full lifecycle parity for reasoning summaries, assistant messages, standard function calls, custom freeform tools (`apply_patch`), `tool_search_call`, and autonomous `image_generation_call`.
- **1:1 3-Rung Effort Routing**: Direct mapping of `low`, `medium`, and `high` reasoning effort across Gemini 3.8 Flash, Gemini 4 Argon, and Claude 5.5 models.
- **Quantized Context Budgeting & Prefix-Cache Protection**: Deterministic 16-turn epoch quantization preserving 95%+ prefix cache hit rates.
- **Tiered Thought Signature Continuity**: Persistent signature store (`~/.codex/comini_signatures.json`) for historical tool turns.
- **Upstream Policy Neutralization**: Zero-429/400 system instruction and message sanitization.

---

## 2. Authentication & Credential Persistence Invariants
- **Primary Store**: `~/.codex/comini_auth.json` (configurable via `COMINI_AUTH_FILE`).
- **Atomic Writes & Permissions**: Every OAuth2 login, account switch, or token refresh (`GetCredentials` / `ForceRefresh`) must atomically write to a temporary file (`comini_auth.json.tmp` with mode `0600`) and rename it to `comini_auth.json` so refreshed access tokens and expiry timestamps are never lost across restarts.
- **Proactive Refresh & Retry**:
  1. Access tokens are proactively refreshed when remaining validity is `< 3 minutes`.
  2. If upstream CCA returns `HTTP 401 UNAUTHENTICATED`, Comini immediately invokes `ForceRefresh()`, persists the new token, and retries the request transparently.

---

## 3. Upstream Google CCA & Policy Invariants

### 3.1 Prompt Sanitization & Neutral Identity
- Google CCA applies real-time filters against proprietary third-party harness signatures (`based on GPT-5`, `Claude Agent SDK`, `x-anthropic-billing-header`, `# Codex desktop context`).
- **Rule**: [`SanitizeSystemInstruction`](internal/harness/sanitizer.go) and [`SanitizeMessageText`](internal/harness/sanitizer.go) neutralize proprietary identity headers to `"You are an expert AI software engineering assistant."` and `"# Desktop client context"` while preserving 100% of user instructions, coding rules, skills, memory citations, and active plans.

### 3.2 Thought Signature Continuity
- Google CCA requires all historical `functionCall` parts inside `model` turns to carry a valid `thoughtSignature`.
- **Rule**:
  1. Record every `thoughtSignature` emitted during streaming into `SignatureStore` (`~/.codex/comini_signatures.json`).
  2. Perform tiered lookups: `call_id` -> `item_id` -> `(session_id, func_name, canonical_args)` -> deterministic `FindAnyCall(func_name, canonical_args)`.
  3. **Fallback Invariant**: If a historical function call lacks a signature, downgrade the pair to model text `[Action: called <tool> with <args>]` and user text `[Observation: <tool> returned <resp>]`. Never emit unsigned `functionCall` parts.

### 3.3 Gemini JSON Schema Restrictions
- Gemini `FunctionDeclaration` schemas do not support `$ref`, `pattern`, `anyOf`, `oneOf`, `allOf`, or `additionalProperties`.
- **Rule**: [`cleanSchemaNode`](internal/schema/compiler.go) strips unsupported keywords, normalizes types to uppercase (`OBJECT`, `STRING`, `INTEGER`, `NUMBER`, `BOOLEAN`, `ARRAY`), maps `nullable: true`, and ensures `OBJECT` schemas always define non-nil `properties: {}`.

---

## 4. Responses API 2026 Protocol Invariants

### 4.1 SSE Event Sequence
Every SSE response stream must follow the strict event lifecycle:
1. `event: response.created`
2. `event: response.output_item.added`
3. Reasoning: `response.reasoning_summary_part.added` -> `response.reasoning_summary_text.delta` -> `response.reasoning_summary_text.done` -> `response.reasoning_summary_part.done` -> `response.output_item.done`
4. Message: `response.content_part.added` -> `response.output_text.delta` -> `response.output_text.done` -> `response.content_part.done` -> `response.output_item.done`
5. Function Call: `response.function_call_arguments.delta` -> `response.function_call_arguments.done` -> `response.output_item.done`
6. Custom Tool Call (`apply_patch`): `response.custom_tool_call_input.delta` -> `response.custom_tool_call_input.done` -> `response.output_item.done`
7. `event: response.completed` (with full `output` items array and `usage` breakdown including `cached_tokens` and `reasoning_tokens`).
8. `data: [DONE]`

### 4.2 Active Turn Dynamic Tool Discovery
- Deferred MCP tools injected via `additional_tools` or `tool_search_output` must only be mounted when they appear in the active turn (after the latest user message). Stale dynamic tools from older turns must not be re-declared.

---

## 5. Multimodal & Vision Standards
- Automatically ingest images from base64 Data URLs, Anthropic image blocks, MCP image content blocks, and local image paths (`.png`, `.jpg`, `.jpeg`, `.webp`, `.gif`).
- **Vision Budget & Compression**:
  - Keep at most the **2 most recent images** within a `1,200,000`-character (~300k token) image ceiling.
  - Automatically compress oversized images (`> 600KB` base64) to JPEG (`Quality: 75`).
  - Replace older historical images with lightweight placeholders (`[Image: <mime> previously inspected and processed in earlier turn]`).

---

## 6. Context Budgeting & Prefix-Cache Protection
- **Context Ceiling**: Aligned with ChatGPT / Codex standards (`model_context_window = 272,000`, `model_auto_compact_token_limit = 244,800`, `tool_output_token_limit = 10,000`):
  - `maxCharsBudget = 800,000` characters (~200,000 tokens).
  - `targetBudget = 640,000` characters (~160,000 tokens).
  - `activeTurnToolMaxChars = 48,000` characters (~12,000 tokens).
  - `historicalToolMaxChars = 6,000` characters (~1,500 tokens).
- **Prefix-Cache Quantization Invariant**:
  - Never use a sliding window index relative to `len(contents)` on every turn.
  - Both historical tool output decay (`quantizedBoundary := ((len(contents) - 5) / 16) * 16`) and turn pruning (`cutoffIndex := (rawCutoff / 16) * 16`) must advance strictly on **16-turn epoch boundaries** so the prefix byte stream stays 100% identical across 16 consecutive turns.
- **Preservation Invariant**: Always preserve Turn 0, user conversational instructions (up to the 10 most recent key user prompts when pruned), `<oai-mem-citation>`, `[Active Technical Plan]`, `[Updated Thread Goal]`, and `[User Confirmed Decisions & Answers]`.
