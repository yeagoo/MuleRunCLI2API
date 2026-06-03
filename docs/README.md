# cli2api docs

Bilingual (English / 简体中文) documentation site for
[cli2api](https://github.com/yeagoo/MuleRunCLI2API), built with
[Fumadocs](https://fumadocs.dev) + Next.js. Icons use Phosphor's duotone
weight; the primary accent is amber-yellow.

## Develop

```sh
pnpm install
pnpm dev          # http://localhost:3000  (→ /en, /cn)
```

Content lives in `content/docs/*.mdx` (English) and `*.cn.mdx` (Chinese), one
pair per page. `meta.json` controls sidebar order and per-page icons (Phosphor
icon names like `Rocket`, resolved to duotone in `lib/source.ts`).

## Build (static export)

```sh
pnpm build        # → ./out  (fully static, no server runtime)
```

The site is configured for **static export** (`output: 'export'` in
`next.config.mjs`): every page is prerendered, and search runs client-side via
an Orama index baked into `out/api/search` (Chinese uses a mandarin tokenizer).

## Deploy to Cloudflare Pages

This is a static site — Cloudflare Pages serves `out/` directly, no Workers
runtime needed.

**Option A — Wrangler CLI:**

```sh
pnpm install && pnpm build
npx wrangler pages deploy out
```

**Option B — Dashboard (Git integration):** connect the GitHub repo and set

| Setting | Value |
|---------|-------|
| Root directory | `docs` |
| Build command | `pnpm install && pnpm build` |
| Build output directory | `out` |

`wrangler.toml` already pins `pages_build_output_dir = "out"`.

### i18n routing on a static host

Static export can't run middleware, so locale detection is replaced by a fixed
redirect in `public/_redirects` (copied to `out/_redirects`):

```
/        /en/            302
/docs/*  /en/docs/:splat 302
```

`/en/*` and `/cn/*` are prerendered and served as-is; the language switcher in
the top bar moves between them. Direct visitors to `/` land on English.

Other static hosts (Netlify, GitHub Pages, S3+CloudFront) work too — point them
at `out/`. The `_redirects` syntax is Cloudflare/Netlify-specific; adapt it for
other hosts.

## Layout

| Path | Purpose |
|------|---------|
| `content/docs/**` | MDX content, `.mdx` (en) + `.cn.mdx` (cn) + `meta.json` |
| `lib/source.ts` | Content loader + Phosphor duotone icon resolver |
| `lib/i18n.ts` | `defineI18n({ en, cn })` |
| `lib/layout.shared.tsx` | Dual-language nav + `zhCN` UI translations |
| `app/[lang]/` | Locale-scoped routes (layout, home, docs) |
| `components/search.tsx` | Client-side static Orama search (en + cn) |
| `components/phosphor-provider.tsx` | Sets duotone as the default icon weight |
| `app/global.css` | Yellow primary override + inline-code wrapping |
