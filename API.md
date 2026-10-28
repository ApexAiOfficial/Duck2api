# Duck2API API Reference

Duck2API provides OpenAI-compatible endpoints backed by DuckDuckGo AI Chat. Compatibility is implemented by this gateway; it is not an official DuckDuckGo API.

## Basics

- **Base URL:** `http://localhost:8080`
- **Authentication:** `Authorization: Bearer <key>` when the `Authorization` environment variable is configured
- **Default response type:** `application/json`; streaming uses Server-Sent Events

## Contents

1. [Chat Completions](#1-chat-completions)
2. [Responses API](#2-responses-api)
3. [Image generation](#3-image-generation)
4. [Image editing](#4-image-editing)
5. [File management](#5-file-management)
6. [Audio transcription](#6-audio-transcription)
7. [Text to speech](#7-text-to-speech)
8. [Models](#8-models)
9. [Health checks](#9-health-checks)

---

## 1. Chat Completions

### `POST /v1/chat/completions`

Send a conversation to an available model; the gateway supports optional reasoning modes and web search.

| Field | Type | Required | Description |
|---|---|---|---|
| `model` | string | Yes | Model identifier; consult `GET /v1/models` |
| `messages` | array | Yes | Conversation messages |
| `stream` | boolean | No | Enable streaming; defaults to `false` |
| `reasoning_effort` | string | No | `none`, `low`, `medium`, or `high` |
| `web_search` | boolean | No | Enable upstream web search; defaults to `false` |
| `file_ids` | string[] | No | Previously uploaded files to use as context |

Simple message:

```json
{"role":"user","content":"Hello"}
```

A multimodal message can contain text and images:

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "Describe this image"},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
  ]
}
```

Example:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

For a reasoning request, add `"reasoning_effort":"high"`. For web search, add `"web_search":true`. Streaming responses use OpenAI-style event chunks.

## 2. Responses API

### `POST /v1/responses`

OpenAI Responses-compatible endpoint for input messages and, where supported, function/custom tool calls. Responses may be streamed.

```bash
curl http://localhost:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-luna","input":"Explain a binary search tree.","stream":false}'
```

Tool calling is emulated through model instructions; the upstream chat interface does not guarantee native structured tool output. The gateway handles client tool definitions and presents parsed tool requests in the expected response form, but clients execute the tools.

## 3. Image generation

### `POST /v1/images/generations`

Generate an image from a prompt. The default route uses the gateway's `image-generation` model.

```bash
curl http://localhost:8080/v1/images/generations \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"A cheerful duck sitting beside a pond"}'
```

Example response structure:

```json
{"data":[{"b64_json":"iVBORw0KGgo...","revised_prompt":"..."}]}
```

Depending on the selected gateway alias, image generation can use either the native image route or a chat-model tool route. The distinction matters because the latter may rewrite the prompt. See [README.md](README.md).

## 4. Image editing

### `POST /v1/images/edits`

Edit an uploaded or base64-encoded image. The current implementation uses the native image model for editing.

JSON input:

```bash
curl http://localhost:8080/v1/images/edits \
  -H 'Content-Type: application/json' \
  -d '{"image":"<base64-image>","prompt":"Replace the background with a beach"}'
```

Multipart input:

```bash
curl http://localhost:8080/v1/images/edits \
  -F 'image=@photo.png' \
  -F 'prompt=Replace the background with a beach'
```

| Field | Type | Required | Description |
|---|---|---|---|
| `image` | string/file | Yes | Base64 image, data URL, or uploaded file |
| `prompt` | string | Yes | Editing instructions |
| `model` | string | No | Accepted for compatibility; does not select the editing route |
| `reasoning_effort` | string | No | Accepted reasoning parameter |

## 5. File management

Upload:

```bash
curl http://localhost:8080/v1/files -F 'file=@document.pdf' -F 'purpose=assistants'
```

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | Yes | File to upload |
| `purpose` | string | No | Purpose; defaults to `assistants` |

Example upload response:

```json
{
  "id": "file-1719398400000000000",
  "object": "file",
  "bytes": 12345,
  "created_at": 1719398400,
  "filename": "document.pdf",
  "purpose": "assistants"
}
```

Additional endpoints:

| Endpoint | Description |
|---|---|
| `GET /v1/files` | List uploaded files |
| `GET /v1/files/:file_id` | Retrieve file metadata |
| `DELETE /v1/files/:file_id` | Delete a file |
| `GET /v1/files/:file_id/content` | Download file contents |

List files:

```bash
curl http://localhost:8080/v1/files
```

Delete a file:

```bash
curl -X DELETE http://localhost:8080/v1/files/file-xxx
```

Download:

```bash
curl http://localhost:8080/v1/files/file-xxx/content -o output.pdf
```

File Q&A workflow: upload a file, capture its `file_id`, then include that ID under `file_ids` in your Chat Completions request.

## 6. Audio transcription

### `POST /v1/audio/transcriptions`

Whisper-compatible transcription interface backed by Duck.ai's dictation feature.

```bash
curl http://localhost:8080/v1/audio/transcriptions \
  -F 'file=@audio.webm' \
  -F 'model=whisper-1'
```

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | Yes | Audio data |
| `model` | string | No | Compatibility parameter; Duck.ai provides the transcription |

Supported formats:

| Audio format | MIME type |
|---|---|
| WebM | `audio/webm` |
| OGG | `audio/ogg` |
| MP3 | `audio/mpeg` |
| WAV | `audio/wav` |
| M4A | `audio/mp4` |
| FLAC | `audio/flac` |
| Opus | `audio/opus` |
| AAC | `audio/aac` |

Response example:

```json
{"text":"An example transcription result"}
```

## 7. Text to speech

### `POST /v1/audio/speech`

The speech endpoint uses Duck.ai's WebRTC session with an OpenAI Realtime model.

| Field | Type | Required | Description |
|---|---|---|---|
| `model` | string | Yes | Compatibility model, such as `tts-1` |
| `input` | string | Yes | Text to synthesize |
| `voice` | string | No | Voice name, e.g. `alloy` (default) |
| `response_format` | string | No | `mp3` (default), `wav`, `ogg`, `flac`, `aac` |
| `speed` | number | No | Accepted but not currently supported |

MP3:

```bash
curl http://localhost:8080/v1/audio/speech \
  -H 'Content-Type: application/json' \
  -d '{"model":"tts-1","input":"Hello world","voice":"alloy","response_format":"mp3"}' \
  --output speech.mp3
```

WAV:

```bash
curl http://localhost:8080/v1/audio/speech \
  -H 'Content-Type: application/json' \
  -d '{"model":"tts-1","input":"Hello world","response_format":"wav"}' \
  --output speech.wav
```

OGG:

```bash
curl http://localhost:8080/v1/audio/speech \
  -H 'Content-Type: application/json' \
  -d '{"model":"tts-1","input":"Test speech","response_format":"ogg"}' \
  --output speech.ogg
```

| Format | Content-Type |
|---|---|
| MP3 | `audio/mpeg` |
| WAV | `audio/wav` |
| OGG | `audio/ogg` |
| FLAC | `audio/flac` |
| AAC | `audio/aac` |

Audio is returned as binary response data. The implementation establishes a WebRTC connection, sends text through a DataChannel, receives Opus audio, and uses FFmpeg where conversion is required. **Install FFmpeg** for MP3/WAV/FLAC/AAC conversion; OGG/Opus requires no transcoding.

## 8. Models

### `GET /v1/models`

Fetch the gateway's model list, generally based on Duck.ai's live `https://duck.ai/duckchat/v1/models` response, with additional local mappings.

```bash
curl http://localhost:8080/v1/models
```

Example response:

```json
{
  "object": "list",
  "data": [{"id":"gpt-5.4","object":"model","created":0,"owned_by":"openai"}]
}
```

The server normally supplies a current Unix timestamp for `created`; `owned_by` comes from upstream provider metadata. Failures should be reported with upstream HTTP status and error information.

The available model set is dynamic. Previously documented examples include:

| Type | Models |
|---|---|
| General | `claude-haiku-4-5`, `mistral-small-2603`, `tinfoil/gemma4-31b` |
| Reasoning | `gpt-5.4-mini`, `gpt-5.6-luna`, `gpt-6-luna` |

Availability is not guaranteed. Prefer `GET /v1/models` and verify requests against your current upstream access.

## 9. Health checks

### `GET /`

```bash
curl http://localhost:8080/
```

Example response: `{"message":"Hello, world!"}`

### `GET /ping`

```bash
curl http://localhost:8080/ping
```

Example response: `{"message":"pong"}`

---

## Environment variables

| Variable | Description | Default |
|---|---|---|
| `Authorization` | Local API key | Unset; authentication disabled |
| `PROXY_URL` | Outbound proxy | Unset |
| `PREFIX` | URL prefix | Unset |
| `TLS_CERT` | TLS certificate path | Unset |
| `TLS_KEY` | TLS key path | Unset |

Existing proxy configuration precedence: `PROXY_URL`, `proxies.txt` (one URL per line), then `http_proxy`.

## HTTP errors

| Status | Meaning |
|---|---|
| 200 | Successful response |
| 400 | Invalid request / input limit |
| 401 | Authentication required or invalid |
| 404 | Missing resource |
| 418 | Upstream request rejected/challenged |
| 429 | Upstream rate limited |
| 500 | Gateway or upstream failure |

The gateway contains handling for 418/429; a response code does not guarantee automatic recovery.

## Implementation notes

**Image generation:** The gateway uses Duck.ai chat endpoints and image-specific tool selection or native image models. SSE messages can contain image-bearing UI components and base64 JPEG payloads; partial images are not returned as the final result.

**Reasoning:** The gateway translates `reasoning_effort` into the upstream `reasoningEffort` setting according to its current mapping. Not all upstream effort values correspond one-to-one with OpenAI conventions.

**Transcription:** Audio bytes are sent to Duck.ai's dictation endpoint with the corresponding content type and the gateway's request authentication state.

**Speech:** WebRTC connects using ICE-server and session-signaling endpoints, uses the `pion/webrtc` library, and returns audio from its data/media channels. FFmpeg performs format conversion where required.

**Experimental status:** These endpoints depend on undocumented upstream behavior; they have not been independently revalidated during this English localization.
