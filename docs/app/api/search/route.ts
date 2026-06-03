import { source } from '@/lib/source';
import { createFromSource } from 'fumadocs-core/search/server';
import { createTokenizer } from '@orama/tokenizers/mandarin';

// Static export: the search index is built at compile time and served as a
// static JSON file; the actual querying happens client-side (see
// components/search.tsx). `staticGET` + revalidate:false makes the route a
// fully static asset.
export const revalidate = false;

export const { staticGET: GET } = createFromSource(source, {
  localeMap: {
    en: { language: 'english' },
    cn: {
      components: {
        tokenizer: createTokenizer(),
      },
      search: {
        threshold: 0,
        tolerance: 0,
      },
    },
  },
});
