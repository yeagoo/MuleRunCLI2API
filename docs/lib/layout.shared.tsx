import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { uiTranslations } from 'fumadocs-ui/i18n';
import { zhCN } from '@fumadocs/language/zh-cn';
import { i18n } from '@/lib/i18n';
import { appName, githubUrl } from '@/lib/shared';

export const translations = i18n
  .translations()
  .extend(uiTranslations())
  .preset('cn', zhCN())
  .add('ui', {
    cn: {
      displayName: '简体中文',
    },
  });

export function baseOptions(locale: string): BaseLayoutProps {
  return {
    nav: {
      title: appName,
      url: `/${locale}`,
    },
    githubUrl,
    links: [
      {
        type: 'main',
        text: locale === 'cn' ? '文档' : 'Documentation',
        url: `/${locale}/docs`,
      },
    ],
  };
}
