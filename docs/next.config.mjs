import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();

/** @type {import('next').NextConfig} */
const config = {
  // Static export for Cloudflare Pages (pure static hosting).
  output: 'export',
  reactStrictMode: true,
  // Static hosts serve /foo/ as /foo/index.html; trailing slash keeps
  // relative asset/link resolution consistent.
  trailingSlash: true,
};

export default withMDX(config);
