import type { Operation } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { lazy, Suspense, useState } from 'react';
import type { ComponentType } from 'react';
import { ErrorBoundary } from '../components/ErrorBoundary';
import type { CommandPaletteProps } from './CommandPalette';

type PaletteModule = { default: ComponentType<CommandPaletteProps> };

// The palette (and its CSS) is fetched on first open, not with the entry chunk.
const loadPalette = (): Promise<PaletteModule> => import('./CommandPalette');

export interface PaletteHostProps {
  open: boolean;
  runningOps: Operation[];
  onClose: () => void;
  /** Fetches the palette module; tests substitute a failing one. */
  load?: () => Promise<PaletteModule>;
}

/** Mounts the lazily loaded command palette on its first open and keeps it mounted afterwards. */
export function PaletteHost({ open, runningOps, onClose, load = loadPalette }: PaletteHostProps) {
  const [requested, setRequested] = useState(false);
  // React.lazy remembers a rejected import, so a failed load gets a fresh lazy component for the next open.
  const [Palette, setPalette] = useState(() => lazy(load));
  if (open && !requested) setRequested(true);
  if (!requested) return null;

  function onLoadError() {
    setRequested(false);
    setPalette(() => lazy(load));
    onClose();
    toasts.push({ kind: 'danger', message: 'Could not open the command palette' });
  }

  return (
    <ErrorBoundary fallback={null} onError={onLoadError}>
      <Suspense fallback={null}>
        <Palette open={open} runningOps={runningOps} onClose={onClose} />
      </Suspense>
    </ErrorBoundary>
  );
}
