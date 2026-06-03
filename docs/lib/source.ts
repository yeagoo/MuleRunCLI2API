import { loader } from 'fumadocs-core/source';
import { createElement } from 'react';
import {
  House,
  Rocket,
  Download,
  Plugs,
  ChatCircle,
  Image as ImageIcon,
  VideoCamera,
  MusicNotes,
  Cube,
  Gear,
  Cloud,
  Wrench,
} from '@phosphor-icons/react/dist/ssr';
import type { Icon } from '@phosphor-icons/react';
import { docs } from 'collections/server';
import { i18n } from '@/lib/i18n';

// Only the icons actually referenced from frontmatter / meta.json. A namespace
// import of the whole Phosphor barrel breaks the production server bundle, and
// an explicit map is lighter anyway.
const icons: Record<string, Icon> = {
  House,
  Rocket,
  Download,
  Plugs,
  ChatCircle,
  Image: ImageIcon,
  VideoCamera,
  MusicNotes,
  Cube,
  Gear,
  Cloud,
  Wrench,
};

// Resolve `icon: "Rocket"` (frontmatter / meta.json) to a Phosphor icon in
// duotone weight. Unknown names render nothing rather than crashing the tree.
function phosphorIcon(name?: string) {
  if (!name) return undefined;
  const Icon = icons[name];
  if (!Icon) return undefined;
  return createElement(Icon, { weight: 'duotone' });
}

// See https://fumadocs.dev/docs/headless/source-api for more info
export const source = loader({
  baseUrl: '/docs',
  source: docs.toFumadocsSource(),
  i18n,
  icon: phosphorIcon,
});
