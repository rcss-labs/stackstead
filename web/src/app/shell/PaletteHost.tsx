import { lazy, Suspense, useState } from 'react';

// The palette (and its CSS) is fetched on first open, not with the entry chunk.
const CommandPalette = lazy(() => import('./CommandPalette'));

export interface PaletteHostProps {
  open: boolean;
  onClose: () => void;
}

/** Mounts the lazily loaded command palette on its first open and keeps it mounted afterwards. */
export function PaletteHost({ open, onClose }: PaletteHostProps) {
  const [requested, setRequested] = useState(false);
  if (open && !requested) setRequested(true);
  if (!requested) return null;
  return (
    <Suspense fallback={null}>
      <CommandPalette open={open} onClose={onClose} />
    </Suspense>
  );
}
