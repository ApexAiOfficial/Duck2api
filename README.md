# Duck2API

An OpenAI-compatible API gateway backed by DuckDuckGo AI Chat. It supports chat completions, the Responses API, image generation and editing, uploaded-file Q&A, transcription, text-to-speech, reasoning modes, web search, and experimental client-side tool calling.

> **Experimental integration:** Duck.ai does not provide a supported public API for this gateway. The upstream website and model availability can change without notice. Follow the provider's terms and usage limits.

## API documentation

See [API.md](API.md) for endpoint reference and curl examples.

## Deployment

### Build from source

```bash
git clone https://github.com/ApexAiOfficial/Duck2api.git
cd Duck2api
go build -o duck2api ./cmd/duck2api
chmod +x ./duck2api
./duck2api
```

### Docker

```bash
docker run -d --name duck2api -p 8080:8080 ghcr.io/aurora-develop/duck2api:latest
```

**Note:** The image above belongs to the original upstream project, not this fork. Build an image from this repository's Dockerfile to run this fork's code.

### Docker Compose

```bash
git clone https://github.com/ApexAiOfficial/Duck2api.git
cd Duck2api
docker compose up -d --build
```

### Koyeb

Koyeb can deploy this repository using its `Dockerfile`. The runtime image includes FFmpeg and exposes port 8080. The platform's `PORT` setting must match the exposed port. Check Koyeb's current pricing and free-tier availability before deployment.

| Variable | Required | Description |
|---|---|---|
| `Authorization` | No | API authentication key; unset disables authentication. Supply only the key itself, without `Bearer ` in the value. |
| `PROXY_URL` | No | Optional outbound proxy URL. |

## Features

| Feature | Endpoint | Notes |
|---|---|---|
| Chat Completions | `POST /v1/chat/completions` | Streaming and non-streaming |
| Responses API | `POST /v1/responses` | OpenAI-compatible response objects |
| Image generation | `POST /v1/images/generations` | Text to image |
| Image editing | `POST /v1/images/edits` | Image modification |
| File upload | `POST /v1/files` | Files for question answering |
| File management | `GET/DELETE /v1/files/:id` | File information and deletion |
| Transcription | `POST /v1/audio/transcriptions` | Whisper-compatible request |
| Speech | `POST /v1/audio/speech` | MP3, WAV, OGG and other formats |
| Models | `GET /v1/models` | Model discovery |

## Quick start

### Chat

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.6-luna",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

### Reasoning modes

Pass `reasoning_effort` to request a reasoning level. Accepted values include `none`, `low`, `medium`, and `high`; actual behavior depends on the upstream model and the gateway's mappings.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-mini",
    "messages": [{"role": "user", "content": "Prove the Pythagorean theorem."}],
    "reasoning_effort": "high"
  }'
```

### Web search

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-nano",
    "messages": [{"role": "user", "content": "What are the latest technology headlines?"}],
    "web_search": true
  }'
```

### Image generation

By default, the gateway requests Duck.ai's native image model (`image-generation`) through `POST /duckchat/v1/chat`. This is designed to preserve the input prompt instead of having a chat model rewrite it.

```bash
curl http://localhost:8080/v1/images/generations \
  -H "Content-Type: application/json" \
  -d '{"prompt": "A cute cat sitting on a windowsill"}'
```

The `model` parameter may be omitted. Aliases such as `gpt-image-1.5` and `gpt-image-1` select the native route; `gpt-image-2` selects the chat-model tool route. These are gateway aliases based on observed image metadata, **not necessarily upstream model IDs**. Passing an image alias directly to Duck.ai's model endpoint may fail.

When a chat model is selected instead, the gateway requests its `GenerateImage` tool, which can add or rewrite image prompts. The gateway wraps the input in its `StrictImagePrompt` instruction to discourage rewriting. The two routes may use different image generators. Each request returns one final image; intermediate partial-image frames are not treated as final outputs.

### Image editing

```bash
# JSON with base64-encoded image data
curl http://localhost:8080/v1/images/edits \
  -H "Content-Type: application/json" \
  -d '{"image":"<base64-image-data>","prompt":"Change the cat to blue"}'

# Multipart upload
curl http://localhost:8080/v1/images/edits \
  -F "image=@cat.png" \
  -F "prompt=Change the cat to blue"
```

Image input may be a `data:` URL or raw base64. This operation uses the native image model with an image part; the `model` field does not select an alternative editing route. The upstream validates the image and returns one final result.

### Files and Q&A

```bash
curl http://localhost:8080/v1/files \
  -F "file=@document.pdf" \
  -F "purpose=assistants"

# Use the file ID returned by the previous request:
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-nano",
    "messages": [{"role": "user", "content": "Summarize this document."}],
    "file_ids": ["file-xxx"]
  }'
```

### Transcription

```bash
curl http://localhost:8080/v1/audio/transcriptions \
  -F "file=@audio.webm" \
  -F "model=whisper-1"
```

Supported input formats include WebM, OGG, MP3, WAV, M4A, FLAC, Opus and AAC.

### Text to speech

```bash
curl http://localhost:8080/v1/audio/speech \
  -H "Content-Type: application/json" \
  -d '{"model":"tts-1","input":"Hello, world!","voice":"alloy","response_format":"mp3"}' \
  --output speech.mp3
```

Output formats include MP3, WAV, OGG, FLAC and AAC. The underlying implementation uses Duck.ai's WebRTC speech session and FFmpeg where conversion is required.

## Models

The gateway merges the live `/v1/models` upstream response with locally defined models and aliases. The table below documents the fork's previously reported behavior; **it is not a fresh availability guarantee**.

| Model | Type | Notes |
|---|---|---|
| `gpt-6-luna` | General | Added locally even when missing from upstream listing |
| `gpt-5.6-luna` | Reasoning | GPT-5.6 Luna |
| `gpt-5.4-mini` | Reasoning | GPT-5.4 mini |
| `claude-haiku-4-5` | General | Claude Haiku 4.5 |
| `tinfoil/gemma4-31b` | General | Gemma 4 31B |
| `mistral-small-2603` | General | Mistral Small 4 |
| `image-generation` | Images | Native image generation/editing route |
| `gpt-image-1.5` | Image alias | Maps to the native route |
| `gpt-image-2` | Image alias | Uses the chat-tool route |

The previous maintainer excluded several model identifiers from the public listing after observing upstream errors, including `gpt-5.6-terra`, `gpt-5.6-sol`, `claude-sonnet-4-6`, `claude-opus-4-8` and `tinfoil/gpt-oss-120b`. Check `internal/initialize/models.go` and the current upstream response before assuming access. Image-upload support is model-dependent.

## Advanced configuration

Create `config.json` from [config.example.json](config.example.json). Configuration keys are environment variable names:

```json
{
  "SERVER_HOST": "0.0.0.0",
  "SERVER_PORT": "8080",
  "Authorization": "your_key",
  "PROXY_URL": "http://proxy:8080",
  "PREFIX": "/api"
}
```

**Precedence:** explicitly set environment variables override values in `config.json`. The latter is excluded by `.gitignore`.

| Variable | Purpose | Example |
|---|---|---|
| `Authorization` | API key, without a `Bearer` prefix | `your_key` |
| `PROXY_URL` | Optional outbound proxy | `http://proxy:8080` |
| `PREFIX` | URL prefix | `/api` |
| `TLS_CERT` | TLS certificate path | `/path/to/cert.pem` |
| `TLS_KEY` | TLS private key path | `/path/to/key.pem` |
| `MAX_INPUT_TOKENS` | Input budget; may reduce old context when exceeded. Negative values disable the token guard | `7500` |

Previous tests by the fork maintainer suggested upstream request-size limitations around 19,500 characters for some inputs. These limits are unofficial and subject to change. The gateway also has a **64 MB HTTP request-body limit** (see `middlewares.BodyLimit`); multimedia base64 payloads can be substantially larger than their originals.

### Tool calling

The upstream Duck.ai chat interface does not expose a general native function-calling channel. This gateway therefore uses **prompt-emulated tool calling**:

1. Registered client tools are condensed and included in instructions.
2. The model is asked to emit `<tool_call>{"name":"tool_name","arguments":{...}}</tool_call>`.
3. The gateway parses this text and emits standard tool-call response structures for supported client protocols.
4. **The client**, not the gateway, executes the tool and returns the result.

Tool lists are compacted to reduce upstream input size, using names, parameter names/types and short descriptions instead of the complete JSON Schema. Tool output and prior turns are folded into text for subsequent requests.

| API | Tool-call response |
|---|---|
| Chat Completions | `message.tool_calls` or streaming `delta.tool_calls`; `finish_reason: "tool_calls"` |
| Anthropic Messages | `tool_use` content blocks; `stop_reason: "tool_use"` |
| Responses | `function_call` or `custom_tool_call` output items |

**Limitations:** Prompt-emulated tool calls are not as reliable as true model-native tool calling. The previous maintainer reported success with some GPT and Gemma models, but Claude-family models frequently declined the simulated contract. This is not guaranteed to work with Claude Code or other agents.

### Coding-agent examples

The previous fork maintainer reported small end-to-end command runs with:

| Client | Reduced setup | Reported smoke test |
|---|---|---|
| Hermes | `hermes chat -t terminal,file,web` | Ran `date` |
| Codex CLI | `codex exec -c features.skip_host_skill_discovery=true` | Ran `ls -a .` |
| DeepSeek harness | A provider configuration patch | Ran a `bash` command |

These are **historical reports, not independently verified results for this fork**. They also depended on trimming agent prompts and tool catalogs to fit the upstream input budget. In particular, Codex may send custom tools inside `additional_tools` items; these require the gateway to return `custom_tool_call` with an `input` field.

Example provider configuration used by the previous maintainer for a lightweight DeepSeek harness:

```yaml
- id: llm-pi-ai
  config:
    providers:
      duckapi:
        baseURL: https://<your-instance>/v1
        apiKeyEnv: DUCKAPI_KEY
        api: openai-completions
        models:
          - id: gpt-5.6-luna
- id: agent-default-model
  config:
    provider: duckapi
    model: gpt-5.6-luna
```

### Outbound proxies and user agents

The gateway can read `proxies.txt` (one URL per line) and use `PROXY_URL` or `http_proxy`. Its existing implementation also derives browser-related headers from the configured user agent, and records an identity line at startup. These are implementation details of the experimental integration, not a supported stability guarantee.

**Important limitations:** The VQD token is still globally shared rather than isolated per outbound identity. Cross-network isolation has not been independently tested. HTTP 418/429 responses can represent upstream restrictions; users should honor rate limits and the service's terms, rather than rely on identity changes as a way to defeat usage restrictions.

## Credits

Thanks to the upstream maintainers and contributors.

Reference project: https://github.com/xqdoo00o/ChatGPT-to-API

## License

MIT License. See [LICENSE](LICENSE).
