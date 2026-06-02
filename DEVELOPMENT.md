# cli2api 开发记录

本文件记录 cli2api 从零到 v2 的关键决策、研究发现与折中。代码本身解释「是什么」，本文件解释「为什么」。

---

## 起点：把 mulerun-cli 的多模态能力变成 OpenAI/Claude 兼容 API

**触发问题**：[`@mulerunai/cli`](https://www.npmjs.com/package/@mulerunai/cli) 在终端可以调用 mulerun 的生文/生图/生视频/生音能力（通过 `mulerun studio`），但应用代码没法直接用 OpenAI / Anthropic SDK 调到这些能力——必须 spawn CLI 或者直接对接 mulerun 各家 vendor 的异步 job API。

**研究 mulerun 后端形态**（通过抓 https://mulerun.com/docs/llms.txt 与多份 OpenAPI yaml）发现：

- **文本生成**：`POST https://api.mulerun.com/v1/chat/completions`（OpenAI 兼容，Bearer 鉴权）与 `POST .../v1/messages`（Anthropic 兼容，X-API-Key 鉴权）已经是标准形态，开箱可用
- **图像 / 视频 / 音频**：走 `POST /vendors/{vendor}/v1/{model}/generation` 提交 + `GET .../{task_id}` 轮询的两段式异步形态，**没有** OpenAI 兼容入口

于是定下目标：开一个本地或可容器化的 HTTP 服务 `cli2api`，统一暴露 OpenAI / Anthropic 兼容端点，把所有 mulerun 多模态能力封装出来。

---

## v0：基础骨架

**决策**（用户确认）：
| 维度 | 选定 | 主要理由 |
|---|---|---|
| 技术栈 | Go + chi | 单二进制好分发，类型脚手架轻 |
| 上游认证 | 复用 mulerun-cli 凭证（`MULERUN_TOKEN` 或 `~/.mulerun/`） | 用户不用再注册一套 key |
| 文本生成 | 薄代理（透传到 mulerun 原生端点） | mulerun 已经提供，重写无价值 |
| 视频生成 | 异步 job + 轮询 | 对齐 OpenAI Sora 形态；避免长连接 |
| 图像生成 | **同步**返回（服务端内部轮询） | 对齐 OpenAI `/v1/images/generations` |
| model 字段 | 透传 mulerun 真名，不做别名层 | 减少未来维护一张映射表的代价 |
| 部署 | 单二进制 + Dockerfile | 适配本地和云上两种场景 |

**关键抽象**：
- `internal/registry`：每个模型注册一个 `Model{ID, Vendor, Kind, VendorPath, MapImage/MapVideo/MapAudio}` 条目。新增模型只需要加一行 + 一个 mapper
- `internal/mulerun`：统一 HTTP client + `Submit/Poll/SubmitAndWait` 异步原语 + `Proxy` 流式透传
- `internal/handler`：handler 层只负责入参 schema 校验 + 调 mapper + 调 client，不持有业务规则
- `pkg/apierr`：OpenAI / Anthropic 双风格错误体，根据请求路径选用

**容易踩的坑**：
- 不要用 `httputil.ReverseProxy` 做代理 —— 它会保留入站 `Authorization` 头，而我们需要替换成 mulerun token
- SSE 流式必须 `Flusher.Flush()` 每块下推，否则客户端体验是「整段一次性返回」
- mulerun 上游对 `Authorization` vs `X-API-Key` 区分严格：chat completions 是 Bearer，messages 是 X-API-Key

**交付**：38 个模型注册（5 图像 + 33 视频）、5 个端点（chat/messages/images/videos/models）、单二进制 7 MB（CGO_ENABLED=0 distroless）。

---

## v1：speech / music 端点 + libsql 持久化

**触发研究**：直接 `npm pack @mulerouter/core@latest` 反推 mulerun 真实注册表（llms.txt 没列出 speech/music 的 yaml）。发现：

1. **minimax** 提供 4 个音频模型：`speech-2.8-hd`、`speech-2.8-turbo`（TTS）、`music-2.0`、`music-2.5`（音乐）。结果字段是 `audios`
2. **状态枚举我之前判断不全**：实际是 `pending | queued | running | processing | completed | succeeded | failed`，我的 Poll 之前漏了 `succeeded` 和 `running` / `queued`
3. **speech 支持 `output_format: "url"`** 拿到下载链接，完美对齐 OpenAI `/v1/audio/speech` —— 服务端拿到 URL 后再 fetch 一次，把字节流回客户端

**用户决策**：
| 维度 | 选定 |
|---|---|
| `/v1/audio/speech` 返回 | 音频字节（OpenAI 兼容） |
| 音乐 API 形态 | 异步 job（同视频） |
| 视频 job 持久化后端 | libsql（[github.com/tursodatabase/libsql](https://github.com/tursodatabase/libsql)） |

**libsql 集成要点**：
- 用 `github.com/tursodatabase/libsql-client-go` + `modernc.org/sqlite`，**纯 Go，零 CGO**——保持 distroless 单二进制不变
- DSN 自动路由：`file:...` → 本地 libsql；`libsql://host?authToken=...` → Turso/sqld 远端
- video 和 music 共用一张 `jobs` 表，`kind` 列区分

**统一异步 job 抽象**：handler 层抽出 `asyncJobAPI{kind, prefix, objectName, resultField, expectKind}` 模板，POST + GET 两侧逻辑共享。video 和 music 各自只是不同的 mapper 入口 + 不同的 store kind。

**交付**：42 个模型（+4 音频），3 个新端点（speech + music POST/GET），libsql 持久化。

---

## v2：模型补齐、图像编辑、job 过期、/v1/responses、流式 speech

**触发研究**：再次过滤 `@mulerouter/core@0.5.0` registry，发现**之前漏了一批热门模型**：
- `openai/gpt-image-2`（带 `/edit` 动作）
- `google/nano-banana` / `nano-banana-pro` / `nano-banana-2`（都带 `/edit`）
- `klingai/kling-v3`、`kling-v3-omni`（5 个 action）
- `alibaba/happy-horse-1-0-t2v/i2v`
- 统一的 `google/veo` 聚合端点

更要命的是**完全没有图像编辑 surface**：OpenAI 原生 `/v1/images/edits` 没暴露，导致这批 `*/edit` 能力客户端用不到。

加上**两个运维短板**：libsql jobs 无限增长没清理；speech 必须等整段音频下完才转给客户端。

**五个子任务一次做完**：

### 任务 1：补齐 22 个新模型

- 沿用现有 `register(Model{…})` 模式
- `VideoInput` 扩展 `FirstFrame`/`Video`/`KeepAudio`/`MultiPrompt`/`MultiShot`/`ShotType`/`Elements`/`Images`——kling-v3 / v3-omni 字段名跟旧版差异很大（用 `first_frame` 而不是 `image`）
- `ImageInput` 扩展 `AspectRatio`/`Resolution`/`Quality`/`Format`/`WebSearch`
- `veo` 聚合 mapper：`Extra.model` 不在时默认 `veo-3.1`；客户端可在 `extra` 里覆盖
- **风险取舍**：旧的 `veo-3.0` / `kling-v2.6` 等是否还活着没在新版 registry 里查到——保守做法是**不动它们**，新增的聚合端点单独加。后续 curl 实测确认旧端点确实仍返回 401（活着），只是新 registry 不再推荐

### 任务 2：`/v1/images/edits` 端点

**双 content-type 支持**：
- `application/json`：`images` 字段接 URL 数组或 `data:` URI 数组
- `multipart/form-data`：file uploads 即时转 `data:image/<sniffed>;base64,<...>` 注入 `images[]`，走和 JSON 同一条 mapper
- 单文件上限 20 MB，对齐 mulerun studio 约定
- `image`（单数）和 `images`（数组）都接受，`AllImages()` 帮助函数合并

**编辑模型 ID 用 `-edit` 后缀**显式区分 `generation` 和 `edit` 两个 action：`gpt-image-2-edit`、`nano-banana-{,pro-,2-}edit`、`wan2.5-i2i-preview-edit`。理由：避免 handler 里写「如果有 image 就走 edit」的隐式分流——出错点太多。

**响应**：和 `/v1/images/generations` 同形态 `{created, data: [{url}]}`。

**踩坑**：`multipart.Writer.CreateFormFile` 默认给文件 `Content-Type: application/octet-stream` 即便上传的是 PNG。我的 handler 检测到这个值时主动 fallback 到 `http.DetectContentType`。

### 任务 3：job 过期 & Reaper

**libsql migration v2**：
- `ALTER TABLE jobs ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;`
- 用 `PRAGMA user_version` 标记 schema 版本，0→1→2 串行执行，幂等
- 单测 `TestLibSQL_MigrationFromV1` 验证从 v1 库无损升级、旧行 `ExpiresAt` 默认 0（永不过期）

**`StartReaper(ctx, store, interval, log)`**：
- `time.NewTicker` + `context.Done()` 优雅退出
- 错误不致命（只 log warn，不停 goroutine）
- 配置：`CLI2API_JOB_RETENTION`（默 7d）、`CLI2API_REAPER_INTERVAL`（默 1h），任一 `0s` 禁用

**抽象层**：`Store` 接口加 `DeleteExpired(ctx, now int64) (int64, error)`，`Memory` 和 `LibSQL` 都实现。Reaper 独立于 Store 接口本身（只调 `DeleteExpired`）。

**烟测确认**：插入 `expires_at=1` 的合成行 → 2 秒后看到日志 `"reaper sweep","deleted":1`。

### 任务 4：`/v1/responses` 透传

**容易记错**：mulerun 的 OpenAI Agents SDK 兼容端点是 `/vendors/openai/v1/responses`（**不是** `/v1/responses` 像 chat 那样）。proxy 里务必写完整。

SSE 已经被 `Client.Proxy` 处理过，无需新代码——复用现有的 `proxyJSON` helper。

### 任务 5：streaming speech

旧代码：发 GET 拿完整 body，**设置 `Content-Length`**（来自上游），然后 `io.Copy`。问题：某些客户端 SDK 在拿到 `Content-Length` 后必须等到完整字节再播放。

新代码：去掉 `Content-Length` 让 Go 自动用 chunked transfer，加 `Flusher.Flush()` 每 4KB 一推。和 SSE 一个套路。

> 说实话这一项收益最小——真正的 TTFB 大头是上游 mulerun 把整段 speech 在 CDN 后端合成完才返回 URL，这个无解，因为 mulerun task 只有 `completed` 一态才有 URL。但顺手做了。

**v2 交付**：70 个模型（+28），10 个端点（+4），libsql 自动迁移 + reaper，全 81 个单测通过。

---

## 三个小修补

### mask 多文件上传
代码本来就支持（`form.File["mask"]` 在 `parseMultipartEdit` 里已经检查）。是我 v2 总结时记错了。补一个 `TestEdits_MultipartWithMaskFile` 测试用例锁住行为。

### 64 MB 请求体上限
加 `middleware.RequestSize(64 << 20)` 到 chi router。烟测确认：5 MB JSON ✅ 通过，70 MB JSON ❌ 被截断返回 400 `request body too large`。给 image (20 MB) + mask (20 MB) + form overhead 留充足空间。

### 注册表重新同步
重新 `npm pack @mulerouter/core@latest`——还是 0.5.0，没新端点。但发现 `veo-3.x` / `kling-v2.x` / `wan*-spark` 这批我们注册的端点在新版 registry 里被移除了。

**直接 curl 探测**：每个「下架」端点都返回 401（与已知存活的 `gpt-image-2` 相同），只有真不存在的路径才返回 404。结论：**mulerun 在保持向后兼容**，studio CLI 推荐 registry 是产品决策不是 API 下线。**不删任何代码**——cli2api 的覆盖面比 `@mulerouter/core` 推荐的还宽，这反而是优势。

---

## 最终架构

```
cli2api/
├── cmd/cli2api/main.go              入口 + slog + reaper 启停 + 优雅关停
├── internal/
│   ├── config/                      env 解析 + 凭证发现链
│   ├── auth/
│   │   ├── credentials.go           MULERUN_TOKEN → ~/.mulerun/{auth,credentials,token}.json
│   │   └── middleware.go            入站 API key 校验（OpenAI Bearer / Anthropic X-API-Key 双兼容）
│   ├── mulerun/
│   │   ├── client.go                统一 HTTP client + Bearer/X-API-Key 注入
│   │   ├── proxy.go                 chat/messages/responses 流式透传
│   │   └── job.go                   Submit / Poll / SubmitAndWait 异步原语
│   ├── registry/                    模型注册表（types/images/edits/videos/audio/chat）
│   ├── jobstore/
│   │   ├── store.go                 Store 接口 + Memory 实现
│   │   ├── libsql.go                LibSQL 实现 + versioned migration
│   │   └── reaper.go                后台清理 goroutine
│   ├── handler/                     chat/messages/responses/images/edits/videos/speech/music/models
│   └── server/                      chi 路由 + RequestID/Logger/CORS/RequestSize/Recoverer
└── pkg/apierr/                      OpenAI / Anthropic 双风格错误体
```

**对外端点**：
| 路径 | 形态 | 上游 |
|---|---|---|
| `POST /v1/chat/completions` | 透传 + SSE | `/v1/chat/completions` (Bearer) |
| `POST /v1/messages` | 透传 + SSE | `/v1/messages` (X-API-Key) |
| `POST /v1/responses` | 透传 + SSE | `/vendors/openai/v1/responses` (Bearer) |
| `POST /v1/images/generations` | 同步包装 | 任一图像 vendor |
| `POST /v1/images/edits` | 同步包装，JSON + multipart | 任一编辑 vendor |
| `POST /v1/videos` + `GET /v1/videos/{id}` | 异步 job | 任一视频 vendor |
| `POST /v1/audio/speech` | 同步，chunked 音频字节 | minimax TTS |
| `POST /v1/audio/music` + `GET /v1/audio/music/{id}` | 异步 job | minimax music |
| `GET /v1/models` | 列模型 | — |
| `GET /healthz` | 探针 | — |

**配置**：见 `.env.example`。关键开关：
- `CLI2API_API_KEYS`：空 = 不鉴权（本地）；非空 = 入站白名单
- `CLI2API_JOBSTORE_DSN`：空 = 内存；`file:...` = libsql 本地；`libsql://...` = Turso/sqld
- `CLI2API_JOB_RETENTION` / `CLI2API_REAPER_INTERVAL`：任一 `0s` 禁用清理

---

## 反思

**做对的几件事**：
- **registry 设计**：每个 mapper 函数化、独立可测。事实证明从 38 涨到 70 模型时，新增成本几乎为零
- **不做 OpenAI 别名层**：`dall-e-3 → midjourney` 这种映射看着方便，但 mulerun 字段语义跟 OpenAI 偏差太大（dall-e 没有 `quality_extension`，wan 有 `prompt_extend`），强行对齐会撒谎；透传真名让客户端自己选最干净
- **同步 vs 异步的两套形态**：图像 + speech 同步、视频 + music 异步，跟 OpenAI 原生形态对齐，SDK 改 base_url 就能用
- **libsql 而不是 Redis**：单进程场景压根不需要外置组件；纯 Go 驱动让 distroless 单二进制方案保持成立

**走过的弯路**：
- 第一轮把 `auth.Style` 跟 `apierr.Style` 写成同一个类型，跨包引用又互相需要——重构成各包独立类型 + `toAPIErr()` 转换
- multipart 文件的 `Content-Type` 默认是 `application/octet-stream`——白白调用 `DetectContentType` fallback 一次才发现
- 测试用 `multipart.NewWriter` 模拟客户端时，要主动给文件 part 设置 PNG signature 才能让 `DetectContentType` 正常识别
- 一度想把所谓"下架"的端点删了，curl 探测发现它们都活着——**研究上游 registry 不等于研究上游 API**

**没做但应该写下来的**：
- 没接 Whisper / 转录（mulerun 没提供）
- 视频 job 没分页列表 `/v1/videos?limit=…`（等真有用户问再加）
- 没做模型别名层（OpenAI 风格 → mulerun 真名映射），客户端要自己用真名
- 内存 store 在分布式场景不行——但当前没有这个需求

---

## 验证

```bash
go vet ./...
go test ./... -count=1
# 五个包全绿：auth / handler / jobstore / mulerun / registry

make build
MULERUN_TOKEN=mr_xxx CLI2API_JOBSTORE_DSN=file:/tmp/jobs.db ./bin/cli2api
# 启动日志：registered_models:61 / reaper_interval:1h0m0s
```

对每个端点的烟测脚本和预期响应见各次开发对话的 commit message（如果有的话）和测试用例。
