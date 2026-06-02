# cli2api

Expose MuleRun's text / image / video generation capabilities through OpenAI- and Anthropic-compatible HTTP endpoints. Drop-in for the official `openai` / `anthropic` SDKs — point `base_url` at this server and the rest is unchanged.

- **Text** — `/v1/chat/completions`, `/v1/messages`, `/v1/responses` are thin proxies to MuleRun's native OpenAI/Anthropic-compatible endpoints (with SSE streaming preserved).
- **Image** — `/v1/images/generations` and `/v1/images/edits` (OpenAI shape, synchronous; multipart and JSON both supported) wrap MuleRun's async vendor jobs (gpt-image-2, nano-banana family, Wan, Midjourney, …).
- **Video** — `/v1/videos` (POST submit / GET poll) for long-running jobs (Sora, Veo, Kling v3 + v3-omni, Seedance, Wan, happy-horse, …).
- **Speech** — `/v1/audio/speech` (OpenAI shape, synchronous, streams raw audio bytes with chunked transfer encoding) backed by MiniMax TTS.
- **Music** — `/v1/audio/music` (POST submit / GET poll) backed by MiniMax music models.

Upstream credentials are reused from `mulerun login` (`~/.mulerun/`) or `MULERUN_TOKEN`. No separate API key issuance needed.

## Install & run

### Binary
```sh
make build
MULERUN_TOKEN=mr_xxx ./bin/cli2api
```

### Docker
```sh
docker compose up --build
# or:
docker build -t cli2api:dev .
docker run --rm -p 8080:8080 \
  -e MULERUN_TOKEN=$MULERUN_TOKEN \
  cli2api:dev
```

Reuse existing OAuth cache instead of an env token:
```sh
docker run --rm -p 8080:8080 \
  -v $HOME/.mulerun:/home/nonroot/.mulerun:ro \
  cli2api:dev
```

## Configuration

| Env var | Default | Purpose |
| --- | --- | --- |
| `CLI2API_PORT` | `8080` | HTTP listen port |
| `CLI2API_API_KEYS` | *(empty)* | Comma-separated inbound API keys; empty = unauthenticated |
| `CLI2API_JOBSTORE_DSN` | *(empty = in-memory)* | libsql DSN for persisting video/music jobs (e.g. `file:/var/lib/cli2api/jobs.db`, `libsql://…?authToken=…`) |
| `CLI2API_JOB_RETENTION` | `168h` | How long async job rows live before the reaper deletes them. `0s` = never expire |
| `CLI2API_REAPER_INTERVAL` | `1h` | How often the reaper sweeps. `0s` = reaper disabled |
| `MULERUN_TOKEN` | *(read `~/.mulerun/`)* | Upstream MuleRun bearer token |
| `MULERUN_API_BASE_URL` | `https://api.mulerun.com` | Upstream base URL |
| `CLI2API_IMAGE_TIMEOUT` | `5m` | Max wait for sync image/speech generation |
| `CLI2API_POLL_INTERVAL` | `2s` | Initial upstream polling cadence |
| `CLI2API_POLL_MAX_INTERVAL` | `10s` | Upper bound on polling backoff |
| `CLI2API_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

See `.env.example`.

## Endpoints

### `POST /v1/chat/completions` *(OpenAI proxy)*
Transparent proxy to MuleRun. Supports `"stream": true` with SSE.
```sh
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $CLI2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}'
```

### `POST /v1/responses` *(OpenAI Agents SDK proxy)*
Transparent proxy to MuleRun's `/vendors/openai/v1/responses`. Supports `"stream": true` with SSE; `"background": true` for async jobs (poll upstream directly).
```sh
curl http://localhost:8080/v1/responses \
  -H "Authorization: Bearer $CLI2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5","input":"Summarize the Black-Scholes formula in one paragraph."}'
```

### `POST /v1/messages` *(Anthropic proxy)*
Transparent proxy. Accepts `x-api-key` or `Authorization: Bearer`.
```sh
curl http://localhost:8080/v1/messages \
  -H "x-api-key: $CLI2API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4-6","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'
```

### `POST /v1/images/generations` *(OpenAI shape, synchronous)*
The server internally polls MuleRun until the image is ready, then returns OpenAI's standard `{ data: [{ url }] }` envelope.
```sh
curl http://localhost:8080/v1/images/generations \
  -H "Authorization: Bearer $CLI2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"wan2.6-t2i","prompt":"a synthwave fox","size":"1024x1024","n":1}'
```
Supported `model`: `midjourney`, `wan2.6-t2i`, `wan2.6-image`, `wan2.5-t2i-preview`, `wan2.5-i2i-preview`.

### `POST /v1/videos` + `GET /v1/videos/{id}` *(async job)*
```sh
id=$(curl -s -X POST http://localhost:8080/v1/videos \
  -H "Authorization: Bearer $CLI2API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"sora-2","prompt":"a fox jumps over a creek","seconds":"4"}' \
  | jq -r .id)
curl -s http://localhost:8080/v1/videos/$id   # poll until status: completed
```
Supported video models (use the listed name verbatim — `t2v`/`i2v` are distinct entries):
- **OpenAI**: `sora`, `sora-2`, `sora-2-pro`
- **Google**: `veo` (aggregated; set body `model` to pick `veo-3.1`/`veo-3.1-fast`/`veo-3`), `veo3`, `veo-3.0`, `veo-3.1`, `veo-3.1-fast` (dedicated)
- **Kling v2**: `kling-v2.1-master-{text,image}-to-video`, `kling-v2.5-turbo-{text,image}-to-video`, `kling-v2.6-{text,image}-to-video`
- **Kling v3**: `kling-v3-text-to-video`, `kling-v3-image-to-video` (uses `first_frame` instead of `image`)
- **Kling v3 Omni**: `kling-v3-omni-{text,image,reference-image,reference-video}-to-video`, `kling-v3-omni-video-to-video-edit` (last one edits an existing video)
- **ByteDance Seedance**: `seedance-2.0-{text,image,reference}-to-video`, and `seedance-2.0-fast-*`
- **Alibaba Wan**: `wan2.6-t2v`, `wan2.6-i2v`, `wan2.5-t2v-preview`, `wan2.5-i2v-preview`, `wan2.2-t2v-plus`, `wan2.2-i2v-plus`, `wan2.2-i2v-flash`, `wan2.1-vace-plus`, `wan2.1-kf2v-plus`
- **Alibaba happy-horse**: `happy-horse-1-0-t2v`, `happy-horse-1-0-i2v`
- **MuleRouter**: `wan2.5-t2v-spark`, `wan2.5-i2v-spark`, `wan2.6-t2v-spark`, `wan2.6-i2v-spark`
- **Midjourney**: `midjourney-video`

Common video request fields (server forwards what's relevant for the chosen vendor):
`prompt`, `negative_prompt`, `image`, `first_frame`, `last_frame`, `video`, `reference_images`, `images`, `aspect_ratio`, `resolution`, `size`, `duration`, `seconds`, `seed`, `mode`, `sound`, `generate_audio`, `multi_shot`, `multi_prompt`, `shot_type`, `elements`, `keep_audio`. Anything under `extra: {...}` is forwarded verbatim to the upstream body.

### `POST /v1/audio/speech` *(OpenAI shape, synchronous, streams audio bytes)*
The server submits to MiniMax, polls until ready, then streams the resulting audio bytes back with the appropriate `Content-Type`.
```sh
curl http://localhost:8080/v1/audio/speech \
  -H "Authorization: Bearer $CLI2API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"speech-2.8-hd","voice":"Charming_Lady","input":"Hello from cli2api.","response_format":"mp3"}' \
  --output speech.mp3
```
Supported `model`: `speech-2.8-hd`, `speech-2.8-turbo`. Accepted fields: `input` (or `prompt`), `voice` (or `voice_id`), `response_format` (`mp3`/`pcm`/`flac`/`wav`/`opus`/`aac`), `speed`, `vol`, `pitch`, `emotion`, `language_boost`, `sample_rate`, `bitrate`, `english_normalization`.

### `POST /v1/audio/music` + `GET /v1/audio/music/{id}` *(async job)*
Music generation typically takes 30s–2min, so it's an async job like video:
```sh
id=$(curl -s -X POST http://localhost:8080/v1/audio/music \
  -H "Authorization: Bearer $CLI2API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"music-2.5","prompt":"upbeat synthwave","lyrics_prompt":"[verse]\nlight the night"}' \
  | jq -r .id)
curl -s http://localhost:8080/v1/audio/music/$id   # poll until status: completed; audios[] holds URLs
```
Supported `model`: `music-2.0`, `music-2.5`. Accepted fields: `prompt` (style), `lyrics_prompt`, `lyrics_optimizer`, `audio_format`, `sample_rate`, `bitrate`.

### `GET /v1/models`
OpenAI-style catalog of every registered generation model plus the common chat models.

### `GET /healthz`
Liveness probe — always `200 ok`.

## SDK usage

### OpenAI Python
```python
from openai import OpenAI
c = OpenAI(api_key="local-key", base_url="http://localhost:8080/v1")

c.chat.completions.create(model="gpt-5",
    messages=[{"role": "user", "content": "hi"}])

c.images.generate(model="wan2.6-t2i",
    prompt="a vector logo of a fox, flat style",
    size="1024x1024")
```

### Anthropic Python
```python
from anthropic import Anthropic
a = Anthropic(api_key="local-key", base_url="http://localhost:8080")

a.messages.create(model="claude-sonnet-4-6", max_tokens=256,
    messages=[{"role": "user", "content": "hi"}])
```

### Async video / music (any language)
The job ID is namespaced (`video_…` / `audio_…`); store it client-side and poll until `status` is `completed` or `failed`. By default jobs live in memory and are lost on restart — set `CLI2API_JOBSTORE_DSN` to a libsql file (`file:/var/lib/cli2api/jobs.db`) or a remote sqld endpoint (`libsql://host?authToken=…`) to persist.

When persistence is on, the reaper (`CLI2API_REAPER_INTERVAL`, default 1h) sweeps and deletes job rows older than `CLI2API_JOB_RETENTION` (default 7 days). Set either to `0s` to disable.

## What's not included

- No per-user accounting / quota — the optional `CLI2API_API_KEYS` allow-list is the only inbound gate.
- Speech is synchronous (small audio); music and video are async (long jobs).
- No transcription (`/v1/audio/transcriptions`) — mulerun doesn't expose a Whisper-style endpoint yet.
- Model name aliasing (`dall-e-3 → midjourney` etc.) is intentionally omitted — pass the upstream model name verbatim.

## License

Apache-2.0
