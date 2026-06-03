'use client';

import { IconContext } from '@phosphor-icons/react';
import type { ReactNode } from 'react';

// Phosphor's IconContext is a React context (client-only). Wrapping it in a
// client component lets the server-rendered root layout stay a Server
// Component while every Phosphor icon below defaults to duotone weight.
export function PhosphorProvider({ children }: { children: ReactNode }) {
  return (
    <IconContext.Provider value={{ weight: 'duotone' }}>
      {children}
    </IconContext.Provider>
  );
}
