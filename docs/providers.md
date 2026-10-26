# Providers & Models

An agent's `model` is a model ID such as `vertexai.claude-opus-4-6` or `kimi.kimi-k3`; the prefix selects the provider. Credentials come from `providers.<name>` in `.opencode.json` or from the environment variables below. The full list of model IDs is in the `opencode-schema.json` enum (generated from `internal/llm/models`).

## Supported models

| Provider | Models |
|----------|--------|
| **OpenAI** | GPT-5, O3 Mini, O4 Mini |
| **Anthropic** | Claude 5.5 Opus (1M), Claude 5 Opus (1M), Claude Fable 5.1 (1M), Claude Fable 5 (1M), Claude 4.8 Opus (1M), Claude 4.7 Opus (1M), Claude 5.5 Sonnet (1M), Claude 5 Sonnet (1M), Claude 4.6 Sonnet (1M), Claude 4.6 Opus (1M), Claude 5.5 Haiku (1M), Claude 4.5 Haiku |
| **Google Gemini** | Gemini 3.0 Pro, Gemini 3.0 Flash |
| **AWS Bedrock** | Claude 5.5 Opus (1M)(EU/Global), Claude 5 Opus (1M)(EU/Global), Claude Fable 5.1 (1M)(EU/Global), Claude Fable 5 (1M)(EU/Global), Claude 4.8 Opus (1M)(EU/Global), Claude 4.7 Opus (1M)(EU/Global), Claude 5.5 Sonnet (1M)(EU/Global), Claude 5 Sonnet (1M)(EU/Global), Claude 4.6 Sonnet (1M)(EU/Global), Claude 4.6 Opus (1M)(EU/Global), Claude 5.5 Haiku (1M)(EU/Global), Claude 4.5 Haiku (EU/Global), Kimi K3 (1M)(US/Global) |
| **VertexAI** | Gemini 3.0 Pro, Gemini 3.0 Flash, Claude 5.5 Opus (1M), Claude 5 Opus (1M), Claude Fable 5.1 (1M), Claude Fable 5 (1M), Claude 4.8 Opus (1M), Claude 4.7 Opus (1M), Claude 5.5 Sonnet (1M), Claude 5 Sonnet (1M), Claude 4.6 Sonnet (1M), Claude 4.6 Opus (1M), Claude 5.5 Haiku (1M), Claude 4.5 Haiku |
| **YandexCloud** | Alice AI LLM, YandexGPT Pro 5.1, YandexGPT Pro 5, YandexGPT Lite 5, DeepSeek V3.2, Qwen3 235B, Qwen3.5 35B, gpt-oss-120b |
| **Kimi (Moonshot)** | Kimi K3 (1M), Kimi K2.7 Code, Kimi K2.7 Code Highspeed |
| **Local** | Any OpenAI-compatible API |

## Credentials

```json
{
  "providers": {
    "openai": { "apiKey": "..." },
    "anthropic": { "apiKey": "..." },
    "gemini": { "apiKey": "..." },
    "vertexai": {
      "project": "your-project-id",
      "location": "us-central1"
    },
    "yandexcloud": { "apiKey": "..." }
  }
}
```

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic Claude models |
| `OPENAI_API_KEY` | OpenAI models |
| `GEMINI_API_KEY` | Google Gemini models |
| `VERTEXAI_PROJECT` | Google Cloud VertexAI |
| `VERTEXAI_LOCATION` | Google Cloud VertexAI |
| `VERTEXAI_LOCATION_COUNT` | VertexAI token count endpoint (global doesn't support) |
| `AWS_ACCESS_KEY_ID` | AWS Bedrock |
| `AWS_SECRET_ACCESS_KEY` | AWS Bedrock |
| `AWS_REGION` | AWS Bedrock |
| `YANDEXCLOUD_API_KEY` | YandexCloud AI Studio API key |
| `YANDEXCLOUD_FOLDER_ID` | YandexCloud folder ID (required for model URI) |
| `MOONSHOT_API_KEY` | Kimi (Moonshot) models |
| `KIMI_API_KEY` | Alias for `MOONSHOT_API_KEY` |
| `LOCAL_ENDPOINT` | Self-hosted model endpoint |
| `LOCAL_ENDPOINT_API_KEY` | Self-hosted model API key |

## Self-hosted models

```bash
export LOCAL_ENDPOINT=http://localhost:1235/v1
export LOCAL_ENDPOINT_API_KEY=secret
```

```json
{
  "agents": {
    "coder": {
      "model": "local.granite-3.3-2b-instruct@q8_0"
    }
  }
}
```

## LiteLLM proxy

Point a provider at a LiteLLM passthrough route with `baseURL` and pass the proxy key as a header:

```json
{
  "providers": {
    "vertexai": {
      "apiKey": "litellm-api-key",
      "baseURL": "https://localhost/vertex_ai",
      "headers": {
        "x-litellm-api-key": "litellm-api-key"
      }
    }
  }
}
```

## YandexCloud

YandexCloud AI Studio provides an OpenAI-compatible API. Set both environment variables:

```bash
export YANDEXCLOUD_API_KEY="AQVN..."
export YANDEXCLOUD_FOLDER_ID="b1g..."
```

The folder ID is required for constructing model URIs (`gpt://<folder_id>/<model_path>`). If not set, the provider will fail with a clear error message.

## Kimi (Moonshot)

Kimi K3 is served through Moonshot's Anthropic-compatible endpoint (`https://api.moonshot.ai/anthropic`) — the same integration path Moonshot documents for Claude Code — so it gets streaming thinking, tool-call streaming, vision, and reasoning replay out of the box:

```bash
export MOONSHOT_API_KEY="sk-..."   # KIMI_API_KEY works as an alias
```

Kimi K3 reasons by default; when `reasoningEffort` is not set for an agent it resolves to `max` (the only effort level K3 exposes at launch). Override the endpoint with `providers.kimi.baseURL` if needed:

```json
{
  "providers": { "kimi": { "apiKey": "sk-..." } },
  "agents": { "coder": { "model": "kimi.kimi-k3" } }
}
```

Kimi K2.7 Code (`kimi.kimi-k2.7-code`) and its faster, double-priced Highspeed variant (`kimi.kimi-k2.7-code-highspeed`) ride the same endpoint with a 256K context. They accept every effort level from `low` to `max`, so an unset `reasoningEffort` keeps the client default (`high`) instead of being pinned to `max`.

Kimi K3 is also available on AWS Bedrock as `bedrock.kimi-k3` (global profile) and `bedrock.us-kimi-k3` (US profile; there is no EU profile yet). Bedrock serves it with OpenAI chat-completions bodies rather than Anthropic Messages, so it is sent through the OpenAI client, with the requests rewritten onto Bedrock's InvokeModel routes. That means it works through the same `providers.bedrock` config and the LiteLLM Bedrock passthrough as the Claude models. Reasoning goes through the OpenAI-style `reasoning_effort` field but keeps K3's levels: `reasoningEffort` `low`/`high`/`max`, defaulting to `max` as on the `kimi` provider (Bedrock also accepts `medium`; `xhigh` folds to `high`). Bedrock caches the prompt prefix automatically, so no cache breakpoints are needed.
