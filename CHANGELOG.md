# Changelog

All notable changes to this project will be documented in this file.

## [0.3.0] - 2025-05-25

### Tools
- `fetch` now supports binary downloads via `output_path` parameter
- Binary-safe: writes raw bytes directly to disk (images, PDFs, ZIPs, any content)
- `~/` home directory expansion supported in `output_path`
- Auto-creates parent directories
- Without `output_path`: existing text behavior unchanged (JSON, HTML, APIs)
- Updated tool description to guide model toward `output_path` for binary content
- Agent no longer needs `bash + curl` for any HTTP interaction

## [0.2.0] - 2025-05-25

### Pricing & Cost Display
- Pricing sourced from **llm-registry** for all providers — no more hardcoded values
- `ModelMeta` now carries `InputCost`, `OutputCost`, `CacheReadCost`, `CacheWriteCost` ($ per 1M tokens)
- `parseRegistry()` extracts all 4 price fields: `input_cost`, `output_cost`, `cache_input_cost`, `cache_output_cost`
- `ApplyRegistryPricing()` does a second-pass pricing fill for Anthropic and Ollama after their capability APIs run
- `enrichMeta()` applies registry pricing at all 4 fallback tiers
- `stripDateSuffix()` matches versioned model IDs (`claude-sonnet-4-20250514` → `claude-sonnet-4`)
- Footer hides `$` when no pricing data is available (GLM, Kimi, MiniMax, MiMo)
- Footer shows `$0.021 (sub)` for subscription/local providers: `claude-oauth`, `opencode-go`, `ollama`, `ollama-cloud`

### Architecture — Backend/Frontend Separation
- Add `IsSubscription() bool` to `llm.Provider` interface — each provider declares its own billing model
- Add `SetThinkingLevel(level string)` to `llm.Provider` interface — runtime level propagation
- Add `Agent.Provider()` to expose current provider to transport layer
- Removed hardcoded `subPricingProviders` map from CLI — frontend just reads `provider.IsSubscription()`
- Add `ModelSupportsThinking(fullModel string)` public wrapper in providers package

### Thinking Level Fixes
- `/thinking` command now updates provider instance, renderer, and footer **immediately**
- `disable` level fully suppresses thinking: sends `think=false` / `type=disabled` to LLM and hides `• level` from footer
- Footer thinking label shown for **all** models that support it (not just Anthropic)
- `NewCLI` and `/model` switch filter `disable` so renderer never shows it as a label

### Documentation
- Added `AGENTS.md` — full AI agent development guide covering architecture, interfaces, data flow, patterns, and anti-patterns

## [0.1.0] - 2025-05-25

### 🎉 Initial Release

First public release of Harness — a minimal AI agent harness built in pure Go.

### Core
- ReAct loop (Think → Act → Observe → Repeat) with configurable max iterations
- Streaming-first architecture — all providers implement SSE streaming
- Event-driven rendering — agent emits events, transport layer renders
- Per-user conversation history with automatic compaction
- In-memory model cache populated at startup from provider APIs

### Providers
- **Claude OAuth** — use your Claude Pro/Team/Enterprise subscription via `claude auth login`
- **Anthropic** — standard API key authentication
- **OpenAI** — GPT-4o, o1, o3, o4-mini series
- **OpenCode Go** — low-cost open coding models (GLM, Kimi, DeepSeek, Qwen, MiniMax, MiMo)
- **Ollama Cloud** — cloud inference with API key
- **Ollama** — local auto-detection, no config needed

### Thinking
- Extended thinking support across all providers
- Configurable levels: `disable` / `low` / `medium` / `high` / `xhigh`
- Universal level mapping per provider (Anthropic effort, OpenAI reasoning_effort, DeepSeek max, Ollama think flag)
- Thinking displayed with gray border, output with cyan border
- `/thinking` command to view/change level at runtime
- `HARNESS_THINKING` env var override
- DeepSeek `reasoning_content` correctly passed back in multi-turn tool call history

### Tools
- `bash` — shell execution with timeout and error handling
- `read_file` — file reading with offset/limit for large files
- `write_file` — file creation with auto directory creation
- `edit` — atomic find/replace (old_text must be unique)
- `fetch` — native Go HTTP client (GET/POST/PUT/DELETE with headers and body)

### Model Management
- `/model` command — list all available models grouped by provider
- `/model <provider/model>` — switch model at runtime (no restart needed)
- Auto-detection of default model from connected providers
- Model capabilities from: Anthropic API → Ollama `/api/show` → llm-registry (GitHub) → hardcoded → inference by name
- `HARNESS_MODEL` env var override
- Persisted in `~/.harness/settings.json`

### Provider Management
- `/connect <provider>` — connect providers interactively
- `/connect` — list all providers with connection status
- API key providers: masked input with `****`
- Claude OAuth: delegates to `claude auth login`, imports tokens to `~/.harness/credentials.json`
- Ollama: auto-detected at startup (no `/connect` needed)
- Env vars take precedence over stored credentials
- Provider status exposed via `GetProviderStatuses()` for transport layer

### CLI Transport
- ASCII art banner with active model display
- Streaming text rendering with left border (cyan for output, gray for thinking)
- Animated spinner during model thinking (Jade-themed tactical phrases)
- One spinner label per agent turn
- Tool calls with icons (⚡ bash, 📄 read_file, ✏️ write_file, 🔧 edit, 🔍 fetch)
- Tool results with timing and truncation
- Compact footer: `╰ 3.2s ↑1.2k ↓156 R8.0k W1.2k $0.012 0.4%/1.0M opencode-go/deepseek-v4-pro`
- Word-wrap aware rendering (reads terminal width)
- `/help` command with full reference
- `/clear` to reset conversation
- Raw terminal input with Ctrl+V clipboard image paste (macOS/Linux/Windows)
- Image support via file paths in messages

### Configuration
- Zero-config startup — works with `./harness` out of the box
- `~/.harness/credentials.json` — single file for all provider credentials
- `~/.harness/settings.json` — active model + thinking level
- `harness.json` — optional project-level config
- All env vars documented in `/help`

### Architecture
- Single `Provider` interface — streaming only, no dual-mode
- `llm/` — core types, SSE parser, image loader
- `llm/providers/` — all provider implementations + infrastructure
- `llm/registry/` — provider factory (Resolve)
- `agent/` — ReAct loop + event system
- `transport/cli/` — terminal rendering (decoupled from core)
- `tools/` — tool registry + implementations
- Model capabilities: 3-tier resolution (API → llm-registry → hardcoded → inference)
- ~9MB single binary, 1 dependency (`charmbracelet/x/term`)
