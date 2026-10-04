# Comini: System Rules, Invariants & Engineering Standards

## 1. Project Mission & Architecture
Comini is an ultra-fast, zero-overhead native Go proxy bridge that connects the **Codex Desktop / ChatGPT Harness (Responses API 2026)** to **Google Cloud Code Antigravity (CCA / Gemini Models)**.

Comini provides full parity with native model providers, including:
- Responses API 2026 SSE Streaming Protocol.
- Full Tool Calling (Standard functions, Namespaced tools, MCP tools, and Custom freeform tools like `apply_patch`).
- Deep Multimodal / Vision ingestion (Data URLs, raw base64, Anthropic image objects, MCP images, local file paths).
- Tiered Thought Signature caching & replaying for Gemini tool continuity.
- Dynamic Context Budgeting & Compaction protection.
- Upstream Google Safety/Policy Neutralization (Zero 429/400 prompt rejections).

---

## 2. Zero-Bypass & Quality Policy (Strictly Enforced)
- **No Shallow Work / Làm cho có**: Every component must handle real-world payloads, edge cases, error states, and network failures.
- **Zero Invariant Bypassing**: Under no circumstances should an invariant check or sanitization step be skipped or commented out.
- **Test-Driven Verification**: All features and schema translations must have automated unit/e2e tests in `tests/`.

---

## 3. Upstream Google CCA & Policy Invariants

### 3.1 Prompt Sanitization & Neutral Identity
- Google CCA applies strict real-time filters against proprietary vendor keywords (`based on GPT-5`, `Claude Agent SDK`, `x-anthropic-billing-header`, etc.).
- When detected, Google CCA rejects requests with HTTP 429 (Resource Exhausted) or HTTP 400.
- **Rule**: `SanitizeSystemInstruction` and `SanitizeMessageText` must neutralize identities to `"You are Codex, a coding agent."` while preserving 100% of user instructions, guidelines, skills rules, memory citations, and technical plans.

### 3.2 Thought Signature Continuity
- Google CCA strictly requires all historical `functionCall` parts inside `model` turns to contain a valid, cryptographically signed `thoughtSignature`.
- **Rule**:
  1. Record every `thoughtSignature` emitted during streaming into `SignatureStore`.
  2. Perform tiered lookups: `call_id` -> `item_id` -> `(session_id, func_name, args)` -> `(func_name, args)`.
  3. **Fallback Invariant**: If a historical function call lacks a valid signature, translate the turn into a model action text `[Action: called <tool> with <args>]` and user observation text `[Observation: tool <tool> returned: <resp>]`. Never send bare unsigned `functionCall` objects that trigger HTTP 400.

### 3.3 Gemini JSON Schema Restrictions
- Gemini function declarations do NOT support OpenAPI keywords: `$ref`, `pattern`, `anyOf`, `oneOf`, `allOf`, `additionalProperties`.
- **Rule**: `cleanSchemaNode` must strip these unsupported attributes, normalize types to uppercase (`OBJECT`, `STRING`, `INTEGER`, `NUMBER`, `BOOLEAN`, `ARRAY`), map `nullable: true` when appropriate, and ensure object types have non-nil `properties: {}`.

---

## 4. Responses API 2026 Protocol Invariants

### 4.1 SSE Event Sequence
All SSE streams must strictly follow the lifecycle:
1. `event: response.created`
2. `event: response.output_item.added`
3. Reasoning: `response.reasoning_summary_part.added` -> `response.reasoning_summary_text.delta` -> `response.reasoning_summary_text.done` -> `response.reasoning_summary_part.done` -> `response.output_item.done`
4. Message: `response.content_part.added` -> `response.output_text.delta` -> `response.output_text.done` -> `response.content_part.done` -> `response.output_item.done`
5. Function Call: `response.function_call_arguments.delta` -> `response.function_call_arguments.done` -> `response.output_item.done`
6. Custom Tool Call (`apply_patch`): `response.custom_tool_call_input.delta` -> `response.custom_tool_call_input.done` -> `response.output_item.done`
7. `event: response.completed` (with full `output` items array and token `usage` breakdown).
8. `data: [DONE]`

### 4.2 Custom Freeform Tools (`apply_patch`)
- When `apply_patch` is invoked, it is a custom freeform tool.
- Emitted in SSE as `type: "custom_tool_call"`, `name: "apply_patch"`, `input: "<raw patch>"`.
- Must never be mangled into a JSON-wrapped function call.

### 4.3 Multi-Agent Namespaced Tools
- Tools with namespaces (e.g., `collaboration:spawn_agent`, `mcp__cua_repl:js`) must preserve the exact `namespace` and `name` attributes in the SSE stream.

---

## 5. Multimodal & Vision Standards
- Comini must automatically detect and ingest images from:
  1. Base64 Data URLs (`data:image/png;base64,...`).
  2. Anthropic style `{ "type": "image", "source": { "type": "base64", ... } }`.
  3. MCP tool output `{ "content": [ { "type": "image", ... } ] }`.
  4. Local image files referenced in prompts (`.png`, `.jpg`, `.jpeg`, `.webp`, `.gif`).
- **Context Hygiene**: Purge raw base64 data from text prompts and function call JSON, replacing them with lightweight status tags (`[Attached Image: image/png loaded into vision encoder]`) to prevent context explosion.

---

## 6. Context Budgeting & Compaction Protection
- Upstream Google CCA has a ~20MB request limit.
- **Preservation Rules**:
  - Always keep Turn 0.
  - Always keep ALL user conversational instructions.
  - Always keep active technical plans (`Plan`), thread goals (`thread_goal_updated`), and verified decisions (`retained_context`).
- **Trimming Rules**:
  - Automatically truncate long intermediate command outputs (> 40 turns old) to head and tail excerpts.
  - If total size exceeds 6MB or 500 turns, prune older intermediate tool execution cycles while keeping the latest 150 turns in full.

---

## 7. Zero-Downtime Safe Operation Protocol
- When Comini is hosting the active Codex conversation, never terminate the running server abruptly.
- Always run tests and compile binaries independently (`go test ./...` and `go build -o comini_bin ./cmd/server`) before replacing the production executable.
