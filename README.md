# Comini: Native Google Antigravity Proxy for Codex Desktop

Comini bridges **Codex Desktop (ChatGPT Harness)** with **Google Cloud Code Antigravity (CCA / Gemini Models)** via a high-performance native Go implementation of the OpenAI Responses API 2026 specification.

## Core Capabilities
- 🚀 **Responses API 2026 Native**: Full SSE lifecycle streaming (`response.created`, `reasoning_summary_text`, `function_call_arguments`, `custom_tool_call_input`, `response.completed`).
- 🛠️ **Universal Tool Calling**: Seamless support for standard tools, custom freeform tools (`apply_patch`), namespaced tools (`collaboration:*`), and MCP tools.
- 🖼️ **Deep Multimodal Vision**: Native ingestion of images from data URLs, base64 strings, Anthropic payloads, MCP responses, and local file paths.
- 🧠 **Thought Signature Store**: Tiered persistent signature caching to ensure Gemini tool call invariants without upstream 400 errors.
- 🛡️ **Zero-429 Neutralizer**: Automatic sanitization of proprietary vendor keywords preventing Google CCA policy rejections.
- 📦 **Context Budgeting**: Intelligent compression of historical tool outputs while preserving 100% of user instructions, technical plans, and memory citations.

## Directory Structure
- `cmd/server/`: Server entrypoint.
- `internal/config/`: Configuration loader.
- `internal/domain/`: Data structures for Responses API and Gemini CCA wire formats.
- `internal/harness/`: System prompt sanitizer and tool contract builder.
- `internal/schema/`: OpenAPI / JSON Schema to Gemini FunctionDeclaration compiler.
- `internal/session/`: Turn builder, signature store, and context budgeting engine.
- `internal/streaming/`: SSE mapper from Gemini streams to Responses API events.
- `internal/transport/`: Google CCA client, multi-endpoint retry logic, and OAuth token manager.
- `tests/`: Automated unit and integration test suite.

## Running Tests & Building
```bash
# Run test suite
go test -v ./tests/...

# Build native binary
go build -o comini ./cmd/server
```

## System Rules & Invariants
See [RULES.md](RULES.md) for strict architectural invariants, zero-bypass policies, and development guidelines.
