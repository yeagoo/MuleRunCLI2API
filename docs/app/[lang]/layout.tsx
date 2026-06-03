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

export default async function Layout({ params, children }: LayoutProps<'/[lang]'>) {
  const { lang } = await params;
  return (
    <html lang={lang} className={inter.className} suppressHydrationWarning>
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
