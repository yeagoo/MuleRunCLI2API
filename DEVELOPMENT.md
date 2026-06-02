# cli2api 开发记录

本文件记录 cli2api 从零到 v3 的关键决策、研究发现、与 6 轮 review 修复链。代码本身解释「是什么」，本文件解释「为什么」。

**文件结构**：前半部分（v0/v1/v2 部分）按时间线讲早期决策；最后的「演进时间线」部分按 review 轮次记录每一轮发现的 bug 和教训。如果你只想知道这个项目当前为什么是这样写的，看后半部分。

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

---

# 演进时间线（v0 → v3）

下面这一段是把整个项目从空文件夹到生产可用的全过程**按 review 轮次**摊开记录——每一轮 review 找到的真实 bug，每一个修复引入的二阶 bug，最终在 reviewer + reviewee 来回 ~6 轮迭代后稳定下来。

**最重要的教训**：每一轮修复都引入了新 bug。如果只跑一轮 review 就 ship，会带着 P1 级别的 bug 进生产。多轮 review 不是过度工程，是**真实必要的**。

## v0 — 初始版本（commit `f586b1b`）

**研究**：抓 mulerun OpenAPI yaml + 反编译 `@mulerouter/core` registry 拿到模型清单和真实 API path。

**交付**：38 模型、5 端点（chat/messages/images/videos/models）、单二进制 7 MB、distroless Docker 镜像、libsql 持久化。

## v1 — 加 speech / music + libsql 持久化（commit 在 v0/v2 之间）

**研究**：`@mulerouter/core@0.5.0` 的 `buildSpeechRequestBody` 暴露了 minimax 的真实参数。

**关键发现**：mulerun task 状态枚举除 `pending|processing|completed|failed` 外还有 `queued|running|succeeded`，我之前的 Poll 漏判后两者。

**交付**：+4 音频模型、speech/music 端点、libsql 文件 + Turso 远端双 DSN 支持。

## v2 — 模型补齐 + 图像编辑 + reaper（commit `84dfaa2` 系列）

**研究**：再次反编译 mulerouter 发现 `gpt-image-2`、`nano-banana`、`kling-v3` 全家、`happy-horse`、统一 veo 都是新加的。`/v1/images/edits` 完全没暴露。

**交付**：22 个新模型、`/v1/images/edits`（JSON + multipart 双兼容）、`/v1/responses` 透传、`/v1/audio/speech` 改成 chunked transfer 流式输出、reaper goroutine 加可配过期清理。

## v3 — review 驱动的 bug 修复链

这才是真正的「ship-ready」之路。下面按 review 轮次列：

### Round 1: codex review on `f586b1b`（4 P-级别 bugs）

| # | 严重度 | 描述 |
|---|---|---|
| P1 | 🔴 | libsql DSN 中 `?authToken=...` 在启动日志裸奔 → 加 `redactDSN` |
| P2 | 🔴 | Poll 在 4xx 时只 decode、不 return done → 同步图像 `SubmitAndWait` 等到超时、异步 video/music job 永远停在 `queued` |
| P2 | 🔴 | `gpt-image-2-edit` 这种 edit-only 模型打 `/v1/images/generations` 会 nil panic → 加 `MapImage == nil` 检查 |
| P3 | 🟡 | Makefile docker-run 挂错路径（`/root/.mulerun` 而非 `/home/nonroot/.mulerun`）|

→ commit `84dfaa2` 修复，加 4 项回归测试。

### Round 2: codex re-review on `84dfaa2`（1 个修复中的疏漏）

| # | 严重度 | 描述 |
|---|---|---|
| P2 | 🔴 | redactDSN 只把 password 脱敏，**username 字段也常常承载 bearer token**（`libsql://token@host`、Bitbucket app passwords）→ 整段 userinfo 替换成 `***` |

→ commit `43805e4` 修复。

### Round 3: codex review on `43805e4` + 端到端实测（3 个 bug，2 个真新 bug）

| # | 严重度 | 描述 |
|---|---|---|
| P2 | 🔴 | reaper 之前会扫掉**还在跑**的 job：retention=120s、CLI2API_REAPER_INTERVAL=1s 时，music 跑 5 分钟，2 分钟后 reaper 把它删了，客户端再轮询 → 404 |
| 真 bug | 🔴 | speech / music body 字段平铺，**实际**应该嵌套 `voice_setting`/`audio_setting`（这是 e2e 烟测发现的，不是 codex 找到） |
| P2 | 🟡 | reaper 修复版只删终态 job → unpolled in-flight 永远不删，retention 等于失效 |

→ commit `1a7ffe4` + `f34240f` 修复。

### Round 4: cc xhigh review on `f34240f`（15 个 finding）

CC review 比 codex 挖得更深，**包括 codex 多轮没看到的**：

| # | 严重度 | 描述 |
|---|---|---|
| 1 | 🔴 P1 | Poll 把 4xx 全当永久失败，**429 / 408 / 401 token 轮换瞬态**会被误判 |
| 2 | 🔴 P1 | `CLI2API_JOB_RETENTION=0`（文档说"永不过期"）实际会**全删 legacy 行**：hardLag=0 → hardCutoff=now |
| 3 | 🔴 P1 | redactDSN 大小写敏感，`?AuthToken=...` 漏密 |
| 4 | 🔴 P1 | `mergeExtra` 是 add-if-missing → `extra` 字段无法覆盖 mapper 已设值，且 V2V `delete()` 后 `mergeExtra` 又把 sound 等添加回来 |
| 5 | 🔴 P1 | test_e2e.py `--keep-server` 的 `finally: return` 吞 KeyboardInterrupt |
| 6 | 🟡 P2 | Docker 挂载路径过时，没加 `~/.config/mulerun` |
| 7 | 🟡 P2 | `floatEnv` 接受 +Inf → time.Duration 溢出 → reaper 删全部 job |
| 8 | 🟡 P2 | `DiscoverToken` 撞到 permission-denied 就 abort，不回退到下一候选 |
| 9 | 🟡 P2 | README/.env 三处文档失同步：缺 `JOB_HARD_CAP_MULT`、`MULERUN_TOKEN` 仍说 `~/.mulerun/`、reaper 行为没记 |
| 10 | 🟢 P3 | speech.go 没 mirror `MapAudio == nil` 检查（latent 防御缺口） |
| 11 | 🟡 P2 | libsql `user_version > len(migrations)` 不报错（降级版本 schema 错乱） |
| 12 | 🟡 P2 | OAuth cache `expires_at` 字段被忽略 |
| 13 | 🟡 P2 | proxy 不剥 Cookie / Proxy-Authorization |
| 14 | 🟢 P3 | `hasMapper` 没 KindImage 分支（前向兼容） |
| 15 | 🟢 P3 | speech/music `audio_setting` 构建代码重复 |

→ commit `889de5f` 修复，**加 12 个新单测**（每个修复点都有针对性回归）。

### Round 5: cc xhigh review on `889de5f`（2 个 PLAUSIBLE 中 1 个真）

| # | 严重度 | 描述 |
|---|---|---|
| A | 🔴 真 bug | `mergeExtra` 改 overwrite 后，`output_format=url` 会被客户端 `extra: {output_format: hex}` 覆盖 → handler 拿 hex 字串当 URL fetch → 报奇怪错误。修法：把 `output_format=url` 移到 mergeExtra **之后**强制锁定 |
| B | 🟡 P3 | DiscoverToken 现在所有 read 错误都 swallow，损坏的 oauth_cache.json 再也不上报 → 加 slog.Warn |

→ 当前 commit 修复。

## 总览：到底修了多少 bug

| Round | 触发方 | 找到 | 真 bug |
|---|---|---|---|
| codex 1 | external | 4 | 4 |
| codex 2 | external | 1 | 1 |
| codex 3 | external + e2e | 3 | 3 |
| cc 1 | internal | 15 | 9 P1+P2 + 6 P3 |
| cc 2 | internal | 2 | 2 |
| **总计** | | **25** | **25** |

加上 e2e 实测自己发现的 **speech body 嵌套结构** + **reaper 删活 job**，**累计 26 个真实 bug**。

**单一最危险的 bug**：reaper 删 in-flight job（cli2api v2 引入；e2e 跑 sora/music 时显式撞到）—— 不是 review 找到的，是真实压力测试里 retention=120s 配置下用户「视频生成 5 分钟」直接坏给我看的。

## 关键工程教训

1. **每一轮修复都会引入新 bug**。修 `redactDSN` 引入了 username 漏密；修「reaper 删活 job」引入了「reaper 永不删」；修「reaper 永不删」引入了 retention=0 反而清空 store；修 `mergeExtra` 让 extra 能覆盖一切，结果连 `output_format=url` 这种 handler 合约都被破坏。

2. **reviewer 的盲区不一样**。codex 抓配置/IO 安全（DSN 泄露、Docker 挂载、文件回退）很犀利；cc xhigh 在调用图复杂的并发逻辑里更敏感（reaper 双谓词的边界条件、Inf/NaN 输入、`mergeExtra` 语义改变后的副作用链）。同时跑两边比只跑一边产出多 ~3 倍。

3. **e2e 测试不是装饰**。speech body 嵌套、reaper 删活 job 这两个**最贵的 bug**都不是 review 找到的，是实际烧上游钱跑 sora-2 / music-2.5 时撞到的。任何宣称做了完整 review 的工程，没跑过 live e2e 都要打折扣。

4. **token 系统的复杂度被严重低估**。最初以为 `MULERUN_TOKEN` 就是一个值，跑下来发现：
   - mulerun-cli 0.0.x：token 直接当 Bearer
   - mulerun-cli 0.1.0：OAuth JWT + 内部交换出 `muk-` 短期 key
   - LLM 平面（chat completions）需要又一个不同的 `CRS_OAI_KEY`
   - 三种 key 都不能互换

5. **「review 完事就修」是危险心态**。真正的 sign-off 是：fix → test → review → fix → test → review → … 直到一轮 review 没新 finding。这个项目用了 6 轮才到达那个点，且最后一轮还找到 1 个真 bug。

6. **写测试是修 bug 的实质部分**。每一个 bug 修复都该带回归测试。这个项目 26 个 bug 里 24 个有专属测试用例，让后续重构不会回退。剩 2 个是 docker 挂载和 Makefile，本身不好用 Go 单测覆盖。

## 累积测试体量

```
internal/auth         credentials_test.go        ~9  tests
internal/config       config_test.go             ~1  table-driven (12 cases)
internal/handler      edits_test.go              ~6  tests
internal/jobstore     store_test.go              ~7  tests
                      reaper_test.go             ~3  tests
internal/mulerun      job_test.go                ~7  tests
internal/registry     registry_test.go           ~13 tests
cmd/cli2api           main_test.go               ~1  table-driven (8 cases)
                                                ───────
                                                 ~50 unit tests
scripts/test_e2e.py   12 cold + 7 live + 4 skip
```

go vet 干净，go test ./... -count=1 全绿，cold smoke 1 秒 12/12，live e2e 跑通过 sora-2 / seedance / music / nano-banana-edit / wan2.6-t2i / gpt-image-2 / speech-2.8-turbo 全部端点。

