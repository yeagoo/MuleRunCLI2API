import Link from 'next/link';
import {
  ChatCircle,
  Image as ImageIcon,
  VideoCamera,
  MusicNotes,
  ArrowRight,
  GithubLogo,
  Lightning,
  Cube,
} from '@phosphor-icons/react/dist/ssr';

const dict = {
  en: {
    tagline: 'OpenAI / Anthropic-compatible API for MuleRun',
    subtitle:
      "Wrap MuleRun's text, image, video, speech and music generation behind drop-in OpenAI- and Anthropic-compatible endpoints. Point your SDK's base_url at it — no code changes.",
    docs: 'Read the docs',
    github: 'GitHub',
    featuresTitle: 'One proxy, every modality',
    features: [
      { icon: ChatCircle, title: 'Text', body: 'chat/completions, messages, responses — with SSE streaming.' },
      { icon: ImageIcon, title: 'Image', body: 'Synchronous generation & editing, OpenAI shape.' },
      { icon: VideoCamera, title: 'Video', body: 'Async submit + poll: Sora, Veo, Kling, Seedance, Wan.' },
      { icon: MusicNotes, title: 'Speech & Music', body: 'MiniMax TTS bytes + async music jobs.' },
    ],
    statsTitle: 'Built for production',
    stats: [
      { icon: Cube, title: '70+ models', body: 'gpt-image-2, nano-banana, sora-2, veo, kling-v3-omni, …' },
      { icon: Lightning, title: 'Single binary', body: '~12 MB, zero CGO, distroless-ready.' },
    ],
  },
  cn: {
    tagline: 'MuleRun 的 OpenAI / Anthropic 兼容 API',
    subtitle:
      '把 MuleRun 的生文、生图、生视频、TTS、音乐能力封装成 OpenAI 和 Anthropic 兼容端点。SDK 改一个 base_url 就能跑——无需改代码。',
    docs: '阅读文档',
    github: 'GitHub',
    featuresTitle: '一个代理，全部模态',
    features: [
      { icon: ChatCircle, title: '文本', body: 'chat/completions、messages、responses —— 含 SSE 流式。' },
      { icon: ImageIcon, title: '图像', body: '同步生成与编辑，OpenAI 形态。' },
      { icon: VideoCamera, title: '视频', body: '异步提交 + 轮询：Sora、Veo、Kling、Seedance、Wan。' },
      { icon: MusicNotes, title: '语音与音乐', body: 'MiniMax TTS 字节流 + 异步音乐 job。' },
    ],
    statsTitle: '为生产打造',
    stats: [
      { icon: Cube, title: '70+ 模型', body: 'gpt-image-2、nano-banana、sora-2、veo、kling-v3-omni 等。' },
      { icon: Lightning, title: '单二进制', body: '~12 MB，零 CGO，distroless 可用。' },
    ],
  },
} as const;

export default async function HomePage({ params }: PageProps<'/[lang]'>) {
  const { lang } = await params;
  const t = dict[lang as keyof typeof dict] ?? dict.en;
  const base = `/${lang}`;

  return (
    <main className="flex flex-1 flex-col items-center px-4 py-20">
      {/* Hero */}
      <div className="flex flex-col items-center text-center max-w-3xl">
        <span className="rounded-full border px-4 py-1.5 text-sm text-fd-muted-foreground mb-6">
          cli2api
        </span>
        <h1 className="text-4xl font-bold tracking-tight sm:text-5xl mb-5">
          {t.tagline}
        </h1>
        <p className="text-lg text-fd-muted-foreground mb-8 leading-relaxed">
          {t.subtitle}
        </p>
        <div className="flex flex-wrap items-center justify-center gap-3">
          <Link
            href={`${base}/docs`}
            className="inline-flex items-center gap-2 rounded-lg bg-fd-primary px-5 py-2.5 font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
          >
            {t.docs}
            <ArrowRight weight="bold" size={18} />
          </Link>
          <a
            href="https://github.com/yeagoo/MuleRunCLI2API"
            className="inline-flex items-center gap-2 rounded-lg border px-5 py-2.5 font-medium transition-colors hover:bg-fd-accent"
          >
            <GithubLogo size={18} />
            {t.github}
          </a>
        </div>
      </div>

      {/* Feature grid */}
      <section className="mt-20 w-full max-w-5xl">
        <h2 className="mb-8 text-center text-2xl font-semibold">{t.featuresTitle}</h2>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {t.features.map((f) => (
            <div
              key={f.title}
              className="rounded-xl border bg-fd-card p-5 transition-colors hover:bg-fd-accent/50"
            >
              <f.icon weight="duotone" size={36} className="text-fd-primary mb-3" />
              <h3 className="font-semibold mb-1">{f.title}</h3>
              <p className="text-sm text-fd-muted-foreground leading-relaxed">{f.body}</p>
            </div>
          ))}
        </div>
      </section>

      {/* Stats */}
      <section className="mt-12 w-full max-w-5xl">
        <h2 className="mb-8 text-center text-2xl font-semibold">{t.statsTitle}</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          {t.stats.map((s) => (
            <div key={s.title} className="flex items-start gap-4 rounded-xl border bg-fd-card p-5">
              <s.icon weight="duotone" size={40} className="text-fd-primary shrink-0" />
              <div>
                <h3 className="font-semibold mb-1">{s.title}</h3>
                <p className="text-sm text-fd-muted-foreground leading-relaxed">{s.body}</p>
              </div>
            </div>
          ))}
        </div>
      </section>
    </main>
  );
}
