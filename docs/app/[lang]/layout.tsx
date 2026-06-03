import '../global.css';
import { RootProvider } from 'fumadocs-ui/provider/next';
import { i18nProvider } from 'fumadocs-ui/i18n';
import { Inter } from 'next/font/google';
import type { Metadata } from 'next';
import { translations } from '@/lib/layout.shared';
import { appName } from '@/lib/shared';
import { PhosphorProvider } from '@/components/phosphor-provider';

const inter = Inter({ subsets: ['latin'] });

export const metadata: Metadata = {
  title: { default: appName, template: `%s · ${appName}` },
  description:
    'OpenAI / Anthropic-compatible HTTP proxy for MuleRun text, image, video, speech and music generation.',
};

// The route slug ('cn') is not a valid BCP-47 language tag; map it to a
// real one for the <html lang> attribute (screen readers, SEO).
const htmlLang: Record<string, string> = { cn: 'zh-CN', en: 'en' };

export default async function Layout({ params, children }: LayoutProps<'/[lang]'>) {
  const { lang } = await params;
  return (
    <html lang={htmlLang[lang] ?? lang} className={inter.className} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <PhosphorProvider>
          <RootProvider i18n={i18nProvider(translations, lang)}>
            {children}
          </RootProvider>
        </PhosphorProvider>
      </body>
    </html>
  );
}
