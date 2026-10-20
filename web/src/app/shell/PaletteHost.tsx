import type { Operation } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { useEffect, useRef, useState } from 'react';
import type { ComponentType } from 'react';
import { Dialog } from '../components/Dialog';
import type { CommandPaletteProps } from './CommandPalette';
import styles from './PaletteHost.module.css';

type PaletteModule = { default: ComponentType<CommandPaletteProps> };

// The palette (and its CSS) is fetched ahead of use or on first open, not with the entry chunk.
const loadPalette = (): Promise<PaletteModule> => import('./CommandPalette');

const PREFETCH_FALLBACK_MS = 1500;

// Set once the idle prefetch has fired, so StrictMode's double effect (and a remount) loads once.
let prefetched = false;
export function resetPalettePrefetchForTests() {
  prefetched = false;
}

export interface PaletteHostProps {
  open: boolean;
  runningOps: Operation[];
  onClose: () => void;
  onNavigate?: (href: string) => void;
  /** Fetches the palette module; tests substitute a controllable one. */
  load?: () => Promise<PaletteModule>;
}

/** A one-input stand-in shown while the palette chunk loads; what is typed here seeds the real palette. */
function PaletteFallback({ draft, onDraft, onClose }: { draft: string; onDraft: (value: string) => void; onClose: () => void }) {
  return (
    <Dialog open onClose={onClose} title="Command palette" width="lg" placement="top">
      <input
        className={styles.input}
        aria-label="Search or run a command"
        placeholder="Jump to a stack, container or run an action…"
        autoComplete="off"
        spellCheck={false}
        value={draft}
        onChange={(event) => onDraft(event.target.value)}
        onKeyDown={(event) => {
          // Nothing can run before the palette exists, and Enter must not pick an entry by accident.
          if (event.key === 'Enter') event.preventDefault();
        }}
      />
      <p className={styles.hint}>Loading…</p>
    </Dialog>
  );
}

/**
 * Mounts the lazily loaded command palette and keeps it mounted once loaded. Until it arrives, an open
 * host shows a plain input so keystrokes are kept, then hands the text over as the palette's first query.
 */
export function PaletteHost({ open, runningOps, onClose, onNavigate, load = loadPalette }: PaletteHostProps) {
  const [mod, setMod] = useState<PaletteModule>();
  const [draft, setDraft] = useState('');
  const loading = useRef(false);
  const loaded = useRef(false);
  const loadRef = useRef(load);
  loadRef.current = load;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Fetch on the first open; a failure closes with a toast and the next open tries again.
  useEffect(() => {
    if (!open || loaded.current || loading.current) return;
    loading.current = true;
    loadRef.current().then(
      (m) => {
        loading.current = false;
        loaded.current = true;
        setMod(m);
      },
      () => {
        loading.current = false;
        setDraft('');
        onCloseRef.current();
        toasts.push({ kind: 'danger', message: 'Could not open the command palette' });
      },
    );
  }, [open]);

  useEffect(() => {
    if (!open) setDraft('');
  }, [open]);

  // Warm the chunk when the browser is idle; a failure here is silent, the first open retries.
  useEffect(() => {
    let cancelled = false;
    const run = () => {
      if (cancelled || prefetched || loaded.current || loading.current) return;
      prefetched = true;
      loading.current = true;
      loadRef.current().then(
        (m) => {
          loading.current = false;
          if (cancelled) return;
          loaded.current = true;
          setMod(m);
        },
        () => {
          loading.current = false;
        },
      );
    };
    if (typeof requestIdleCallback === 'function') {
      const id = requestIdleCallback(run);
      return () => {
        cancelled = true;
        cancelIdleCallback(id);
      };
    }
    const id = setTimeout(run, PREFETCH_FALLBACK_MS);
    return () => {
      cancelled = true;
      clearTimeout(id);
    };
  }, []);

  if (mod) {
    const Palette = mod.default;
    return <Palette open={open} runningOps={runningOps} onClose={onClose} onNavigate={onNavigate} initialQuery={draft} />;
  }
  if (!open) return null;
  return <PaletteFallback draft={draft} onDraft={setDraft} onClose={onClose} />;
}
