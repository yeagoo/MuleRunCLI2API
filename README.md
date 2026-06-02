# cli2api

> 把 [MuleRun](https://mulerun.com) 的生文、生图、生视频、TTS、音乐能力，封装成 **OpenAI 和 Anthropic 兼容**的 HTTP 服务。
> 现有的 `openai` / `anthropic` SDK 改一个 `base_url` 就能跑——无需改业务代码。

单二进制 Go 服务（~7 MB），distroless 镜像可用，零 CGO 依赖。

---

## 它能干什么

| 需求 | 端点 | 说明 |
|---|---|---|
| 文本对话 | `POST /v1/chat/completions` | 透传 mulerun OpenAI 兼容端点，含 SSE |
| Claude 对话 | `POST /v1/messages` | 透传 mulerun Anthropic 兼容端点，含 SSE |
| OpenAI Agents SDK | `POST /v1/responses` | 透传到 mulerun 的 `/vendors/openai/v1/responses` |
| 生成图像 | `POST /v1/images/generations` | 同步返回，跟 OpenAI 一样 |
| 编辑图像 | `POST /v1/images/edits` | JSON + multipart 都支持 |
| 生成视频 | `POST /v1/videos` + `GET /v1/videos/{id}` | 异步 job（sora / veo / kling / seedance 通常 30s–5min） |
| 文字转语音 | `POST /v1/audio/speech` | 同步流式返回音频字节，对齐 OpenAI TTS |
| 生成音乐 | `POST /v1/audio/music` + `GET /v1/audio/music/{id}` | 异步 job |
| 模型清单 | `GET /v1/models` | 所有已注册模型 + 常用 chat 模型 |
| 探针 | `GET /healthz` | `200 ok` |

**70+ 模型已注册**：`gpt-image-2`、`nano-banana` 全家、`sora-2`、`veo`、`kling-v3-omni`、`wan2.6-*`、`midjourney`、`seedance-2.0-*`、`happy-horse-1-0-*`、`speech-2.8-hd`、`music-2.5` 等等。完整列表见下文「[支持的模型](#支持的模型)」或 `GET /v1/models`。

---

## 5 分钟跑起来

### 1. 登录 mulerun，拿到上游凭证

```sh
npm i -g @mulerunai/cli
mulerun login        # 浏览器 OAuth；token 自动存到 ~/.config/mulerun/
                     # （mulerun-cli ≥0.1.0；旧版本是 ~/.mulerun/）
```

或者直接拿到一个 token 后：`export MULERUN_TOKEN=mr_xxxxxxxxxxxxxxx`。

> **关于 token 平面**：`mulerun login` 拿到的 OAuth token 走 **studio 平面**（图像/视频/音频）。如果你想用 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 这三个**文本端点**，需要单独的 LLM gateway key（mulerun 控制台另发）—— 这是 mulerun 自己的两套独立认证系统，不是 cli2api 的限制。详见下文「[排错](#排错)」。

### 2. 构建并启动 cli2api

```sh
git clone <this-repo> cli2api && cd cli2api
make build
./bin/cli2api
```

启动后你会看到：

```json
{"level":"INFO","msg":"startup","addr":":8080","registered_models":61,
 "jobstore":"memory","auth_required":false,
 "token_source":"file:/home/you/.config/mulerun/oauth_cache.json"}
```

`token_source` 字段告诉你它从哪儿读到了凭证。如果是 `env:MULERUN_TOKEN` 你设过环境变量；`file:...` 说明它读了 OAuth cache。如果启动失败显示 `no mulerun credentials found` 就跑一次 `mulerun login` 或显式设环境变量。

### 3. 第一次调用

```sh
curl http://localhost:8080/v1/images/generations \
  -H "Content-Type: application/json" \
  -d '{"model":"wan2.6-t2i","prompt":"a synthwave fox","size":"1024x1024"}'
# → {"created":1733...,"data":[{"url":"https://cdn.mulerun.com/..."}]}
```

完事。

### 4.（可选）入站鉴权

默认无鉴权——仅本地用没问题。要对外发布就加一个白名单：

```sh
CLI2API_API_KEYS=sk-team1,sk-team2 ./bin/cli2api
```

之后每个请求必须带 `Authorization: Bearer sk-team1`（OpenAI 风格）或 `x-api-key: sk-team1`（Anthropic 风格）。

---

## 常见用法

### 生成图像（OpenAI Python SDK）

```python
from openai import OpenAI
c = OpenAI(api_key="local-key", base_url="http://localhost:8080/v1")

r = c.images.generate(
    model="wan2.6-t2i",         # 或 "gpt-image-2", "nano-banana", "midjourney"
    prompt="a vector logo of a fox, flat style",
    size="1024x1024",
)
print(r.data[0].url)
```

### 编辑图像

```python
# 方式一：传 URL 或 data: URI
c.images.edit(
    model="nano-banana-edit",
    prompt="add sunglasses",
    image="https://your.cdn/portrait.png",
)

# 方式二：上传本地文件（multipart，OpenAI 原生形式）
with open("photo.png", "rb") as f:
    c.images.edit(model="gpt-image-2-edit", prompt="oil painting style", image=f)
```

### 生成视频（异步轮询）

视频通常要几十秒到几分钟，cli2api 返回 job ID，由客户端轮询：

```python
import time, httpx

base = "http://localhost:8080"

# 提交
job = httpx.post(f"{base}/v1/videos", json={
    "model": "sora-2",
    "prompt": "a fox jumps over a creek",
    "seconds": "4",
}).json()
job_id = job["id"]              # 形如 "video_a3xq2..."

# 轮询
while True:
    r = httpx.get(f"{base}/v1/videos/{job_id}").json()
    if r["status"] in ("completed", "failed"):
        break
    time.sleep(5)

print(r.get("videos") or r.get("error"))
```

`status` 取值：`queued` / `in_progress` / `completed` / `failed`。

### 文字转语音

```python
audio = c.audio.speech.create(
    model="speech-2.8-hd",
    voice="Charming_Lady",      # MiniMax voice ID
    input="Hello from cli2api.",
    response_format="mp3",      # mp3 / pcm / flac / wav / opus / aac
)
audio.stream_to_file("hello.mp3")
```

返回的是音频字节流（chunked transfer），可以直接边收边播。

### Claude（Anthropic SDK）

```python
from anthropic import Anthropic
a = Anthropic(api_key="local-key", base_url="http://localhost:8080")

r = a.messages.create(
    model="claude-sonnet-4-6",
    max_tokens=256,
    messages=[{"role": "user", "content": "hi"}],
)
print(r.content[0].text)
```

### 生成音乐（异步同视频）

```python
job = httpx.post(f"{base}/v1/audio/music", json={
    "model": "music-2.5",
    "prompt": "upbeat synthwave, melodic",
    "lyrics_prompt": "[verse]\nlight the night",
}).json()
# 然后 GET /v1/audio/music/{id} 轮询直到 status=completed
# 完成后 r["audios"] 是下载链接数组
```

### 流式对话（SSE）

加 `"stream": true`，跟原生 OpenAI / Anthropic 完全一致：

```python
for chunk in c.chat.completions.create(
    model="gpt-5",
    messages=[{"role": "user", "content": "tell me a joke"}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="")
```

---

## 部署

### Docker

```sh
docker compose up --build
```

`docker-compose.yml` 里两种凭证注入方式注释好了：
- **A**：`MULERUN_TOKEN` 环境变量（最简单）
- **B**：挂 `~/.config/mulerun` + `~/.mulerun` 到容器（用宿主的 OAuth cache）

任选其一。注意：mulerun-cli ≥0.1.0 token 在 `~/.config/mulerun/oauth_cache.json`，旧版本在 `~/.mulerun/`，挂哪一个看你装的版本。

### 单二进制 + systemd

```sh
make build
sudo cp bin/cli2api /usr/local/bin/
sudo tee /etc/systemd/system/cli2api.service > /dev/null <<'EOF'
[Unit]
Description=cli2api
After=network.target

[Service]
Environment=MULERUN_TOKEN=mr_xxxxx
Environment=CLI2API_API_KEYS=sk-prod-key-1
Environment=CLI2API_JOBSTORE_DSN=file:/var/lib/cli2api/jobs.db
ExecStart=/usr/local/bin/cli2api
Restart=on-failure
User=cli2api

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable --now cli2api
```

### 持久化 job 存储

默认 jobs 存内存，重启会丢。生产环境建议持久化：

```sh
# 本地 libsql 文件（纯 Go 驱动，零依赖）
CLI2API_JOBSTORE_DSN=file:/var/lib/cli2api/jobs.db ./bin/cli2api

# 远端 Turso / sqld
CLI2API_JOBSTORE_DSN="libsql://your-db.turso.io?authToken=$TURSO_TOKEN" ./bin/cli2api
```

reaper 后台 goroutine 每 `CLI2API_REAPER_INTERVAL`（默认 1h）扫一次，删掉超过 `CLI2API_JOB_RETENTION`（默认 7d）的旧 job。

**保留期细节**（避免在跑的 job 被误删）：
- 终态 job（`completed` / `failed`）：`expires_at` 一过就删
- in-flight job（`queued` / `in_progress`）：保留到 `expires_at + retention × CLI2API_JOB_HARD_CAP_MULT`（默认 3）才强删——给客户端足够时间继续轮询
- `CLI2API_JOB_RETENTION=0` 整体禁用 sweep，**任何 job 都不会被自动删**
- `CLI2API_REAPER_INTERVAL=0` 也直接禁用 reaper 协程

> 启动日志里 DSN 的鉴权部分会自动脱敏，可以放心写入容器日志。

### 反向代理（nginx）

流式端点要关掉 buffering，否则 SSE 和音频流会被卡住：

```nginx
location /v1/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_buffering off;          # SSE + audio streaming
    proxy_read_timeout 600s;      # 长轮询 / 同步图像最长 5 min
    client_max_body_size 64M;     # multipart 图像编辑用
}
```

---

## 配置

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `CLI2API_PORT` | `8080` | HTTP 监听端口 |
| `CLI2API_API_KEYS` | *(空)* | 入站 API key 白名单（逗号分隔）；空 = 不鉴权 |
| `CLI2API_JOBSTORE_DSN` | *(空 = 内存)* | libsql DSN（`file:...` / `libsql://...?authToken=...`） |
| `CLI2API_JOB_RETENTION` | `168h` | job 行保留时长；`0s` = 不过期（reaper 整体跳过 sweep） |
| `CLI2API_REAPER_INTERVAL` | `1h` | reaper 扫描周期；`0s` = 禁用 |
| `CLI2API_JOB_HARD_CAP_MULT` | `3.0` | in-flight job 的硬上限倍数。`expires_at + retention × N` 之后无论状态如何强删，避免被遗弃的 job 无限增长。范围 1.0–1000.0 |
| `CLI2API_IMAGE_TIMEOUT` | `5m` | 同步图像/语音最长等待 |
| `CLI2API_POLL_INTERVAL` | `2s` | 上游轮询初始周期 |
| `CLI2API_POLL_MAX_INTERVAL` | `10s` | 轮询退避上限 |
| `CLI2API_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `MULERUN_TOKEN` | *(读 `~/.config/mulerun/oauth_cache.json` 或 `~/.mulerun/`)* | 上游 mulerun token；不设时按顺序读 OAuth cache（mulerun-cli ≥0.1.0）和 legacy 路径 |
| `MULERUN_API_BASE_URL` | `https://api.mulerun.com` | 上游 base URL |

完整模板见 `.env.example`。

---

## 排错

**`no mulerun credentials found`**
- 跑 `mulerun login`，或者 `export MULERUN_TOKEN=mr_xxx`
- 启动日志的 `token_source` 字段会告诉你它读到的是哪个文件 / 环境变量

**`/v1/chat/completions` 返回 401 `Invalid API Key format`**
- mulerun 的 OAuth/Studio token 不能直接调聊天端点（mulerun 自己的两套独立认证）
- 需要从 mulerun 控制台拿单独的 LLM gateway key，赋给 `MULERUN_TOKEN`
- 图像/视频/音频端点不受影响——它们用 studio plane 的 token 就行

**`502 upstream HTTP 401`**
- 上游 token 过期或被拒。重新登录 / 换 token
- mulerun-cli OAuth cache 默认有 `expires_at` 字段，cli2api 会跳过过期 token 但**最简单还是 `mulerun login` 一遍**

**`vendor_error: code 3005 / 3006 / ...`**
- 这是 mulerun 上游真实错误（minimax/seedance/wan 等服务自身挂了）
- cli2api 的工作正常，把上游错误结构化转给客户端
- 重试或换模型；mulerun 服务恢复后自动好

**`404 unknown image model: dall-e-3`**
- cli2api **不做** OpenAI 模型名到 mulerun 的别名映射。请用 mulerun 真名（`gpt-image-2` / `wan2.6-t2i` / `midjourney`）
- `curl localhost:8080/v1/models | jq '.data[].id'` 看完整列表

**图像生成卡在那里很久才返回**
- 同步包装内部在轮询 mulerun，最长等 `CLI2API_IMAGE_TIMEOUT`（默 5min）
- 大批量（`n=4`）或 4K 分辨率请把超时往上调

**视频 / 音乐 job 状态停在 `queued` 不动**
- 检查 `CLI2API_JOB_RETENTION` 和 `CLI2API_JOB_HARD_CAP_MULT`：retention 太短 + multiplier 太小 = 还没轮询完就被 reaper 删了
- 默认 retention=7d、multiplier=3 完全够用；调小是给短期测试用

**重启后视频 / 音乐 job ID 找不到了**
- 内存 store 重启即丢。生产用 `CLI2API_JOBSTORE_DSN=file:...` 或远端 libsql 持久化

**`request body too large` (400)**
- 单次请求上限 64 MB（image + mask + form overhead 够用）
- 这是 chi `RequestSize` 中间件抛的，handler 抓住后包装成 OpenAI 风格 400

**SSE 流式响应在客户端不出现增量**
- 反向代理没关 `proxy_buffering`，nginx / Caddy 都要单独配
- 自己的客户端代码确认是用 `stream=True` 接 SSE，不是 `json()` 整收

---

## 支持的模型

### 图像（`/v1/images/generations` 同步）
- **OpenAI**: `gpt-image-2`
- **Google**: `nano-banana`, `nano-banana-pro`, `nano-banana-2`
- **Midjourney**: `midjourney`
- **Alibaba Wan**: `wan2.6-t2i`, `wan2.6-image`, `wan2.5-t2i-preview`, `wan2.5-i2i-preview`

### 图像编辑（`/v1/images/edits` 同步）
- `gpt-image-2-edit`（支持 `mask`）
- `nano-banana-edit`, `nano-banana-pro-edit`, `nano-banana-2-edit`
- `wan2.5-i2i-preview-edit`

### 视频（`/v1/videos` 异步）
- **OpenAI**: `sora`, `sora-2`, `sora-2-pro`
- **Google**: `veo`（聚合端点，可在 body 里设 `model` 选 `veo-3.1`/`veo-3.1-fast`/`veo-3`）；`veo3`, `veo-3.0`, `veo-3.1`, `veo-3.1-fast`（dedicated 别名，向后兼容保留）
- **Kling v2**: `kling-v2.1-master-{text,image}-to-video`, `kling-v2.5-turbo-{text,image}-to-video`, `kling-v2.6-{text,image}-to-video`
- **Kling v3**: `kling-v3-text-to-video`, `kling-v3-image-to-video`（用 `first_frame` 而非 `image`）
- **Kling v3 Omni**: `kling-v3-omni-{text,image,reference-image,reference-video}-to-video`, `kling-v3-omni-video-to-video-edit`
- **ByteDance Seedance**: `seedance-2.0-{text,image,reference}-to-video`, `seedance-2.0-fast-*`
- **Alibaba Wan**: `wan2.6-t2v`, `wan2.6-i2v`, `wan2.5-t2v-preview`, `wan2.5-i2v-preview`, `wan2.2-t2v-plus`, `wan2.2-i2v-plus`, `wan2.2-i2v-flash`, `wan2.1-vace-plus`, `wan2.1-kf2v-plus`
- **Alibaba happy-horse**: `happy-horse-1-0-t2v`, `happy-horse-1-0-i2v`
- **MuleRouter**: `wan2.5-t2v-spark`, `wan2.5-i2v-spark`, `wan2.6-t2v-spark`, `wan2.6-i2v-spark`
- **Midjourney**: `midjourney-video`

视频通用字段：`prompt`, `negative_prompt`, `image`, `first_frame`, `last_frame`, `video`, `reference_images`, `images`, `aspect_ratio`, `resolution`, `size`, `duration`, `seconds`, `seed`, `mode`, `sound`, `generate_audio`, `multi_shot`, `multi_prompt`, `shot_type`, `elements`, `keep_audio`。`extra: {...}` 透传到上游。

### 语音 / 音乐
- TTS：`speech-2.8-hd`, `speech-2.8-turbo`（MiniMax）
- 音乐：`music-2.0`, `music-2.5`（MiniMax）

### 文本（透传）
任何 mulerun 支持的 chat / messages / responses 模型都能用——cli2api 不解析 model 字段，原样转发。

---

## FAQ

**为什么不直接调 mulerun API？**
`/v1/chat/completions` 和 `/v1/messages` 本来就 OpenAI/Anthropic 兼容，直接调没问题。但图像/视频/音频走 `/vendors/{vendor}/...` 异步 job 形式，request shape 跟 OpenAI 完全不同。cli2api 把这层差异隐藏掉，让现有 SDK 代码不改就能跑。

**会缓存结果吗？**
不会。每次调用都是新的 mulerun task。要缓存在应用层或 CDN 层做。

**能跑在 Lambda / Cloud Function 里吗？**
文本和图像同步端点可以。视频/音乐显式异步——客户端轮询，serverless 实例随便重启。配合 libsql 持久化 store 即可。

**支持 OpenAI 的 `dall-e-3` / `gpt-4o-audio-preview` 模型名吗？**
**不支持**别名映射，这是设计取舍。模型名要用 mulerun 真名（`gpt-image-2`、`midjourney` 等）。别名会让客户端误以为在调 OpenAI，行为差异会很难排查。

**多租户怎么办？**
没有内置用户/配额。`CLI2API_API_KEYS` 是平面 allow-list。要做配额请放在 API 网关后面。

**SSE 在 nginx 后面不出增量？**
`proxy_buffering off;` —— 见上文「反向代理」段。

**这代码经过几次 review？质量怎么样？**
项目用了 6 轮 reviewer + reviewee 来回（codex 3 轮 + cc 2 轮 + 实战 e2e），累计修复 **26 个真实 bug**——从凭证泄露、异步 job 永久卡死、reaper 误删活 job 到上游 schema 嵌套错误。每一轮修复都引入新 bug，最后一轮 cc review 才完全 clean。详细演进见 [DEVELOPMENT.md](./DEVELOPMENT.md)。
- 50+ 单元测试，每个 bug 都带专属回归
- live e2e 实测：`make test-e2e-live` 跑通 sora-2 / seedance / nano-banana-edit / wan2.6-t2i / gpt-image-2 / speech-2.8-turbo 全部端点
- DSN 鉴权脱敏、reaper 软硬上限、过期 token 自动跳过、4xx 区分瞬态/永久——这些边角都覆盖到了

---

## 开发

```sh
go test ./... -count=1     # 全部单测
go vet ./...
make build                 # 单二进制到 bin/cli2api
```

### 端到端测试

```sh
make test-e2e              # 冷烟测：启服务 + schema/error 路径 + reaper（不打上游，1 秒跑完）
make test-e2e-live-cheap   # 加上真实图像/语音/对话调用（约 $0.10 / 次，约 3 分钟）
make test-e2e-live         # 全量，含 sora / veo 等视频（约 $5–20 / 次，约 13 分钟）
```

- **冷烟测**（12 项）：healthz / models / inbound auth / 错误码 / 64 MB body cap / CORS / reaper 真删过期 job / 关键回归（edit-only 模型不再 panic）
- **Live cheap**（+5 项）：wan2.6-t2i / gpt-image-2 / nano-banana-edit / speech-2.8-turbo / music-2.5
- **Live full**（+2 项）：sora-2 短视频 / seedance-2.0-fast

测试脚本会自动检测 `MULERUN_TOKEN` 类型——`muk-` 前缀的 studio token 会跳过 4 个 chat 测试（不是失败，是 skipped），因为那些端点要 LLM gateway plane 的 token。

详见 `scripts/test_e2e.py`。

开发过程笔记见 [DEVELOPMENT.md](./DEVELOPMENT.md)。

---

## License

Apache-2.0
