import { writable, type Readable } from '$lib/store';
import { ApiError } from '../api/client';

export type ComposeState = {
  phase: 'loading' | 'loadFailed' | 'editing' | 'previewing' | 'saving' | 'conflict' | 'saved';
  content: string;
  draft: string;
  hash: string;
  /** Last failure to show verbatim (422 compose message, network error, or failed load). */
  error?: string;
  /** Server message of a 409, shown verbatim next to Reload / Discard. */
  conflictMessage?: string;
  /** The last successful save skipped some port/IP checks (Docker unreachable). */
  checksPartial?: boolean;
};

export interface ComposeDeps {
  load: () => Promise<{ content: string; hash: string }>;
  save: (content: string, hash: string) => Promise<{ hash: string; checksPartial?: boolean }>;
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * Editing state machine for one stack's raw compose file. Every save sends the
 * hash it loaded; a 409 never overwrites, it asks the user to reload.
 * `stackName` is kept for callers' identification only: I/O goes through deps.
 */
export function createComposeEditor(
  _stackName: string,
  deps: ComposeDeps,
): {
  state: Readable<ComposeState>;
  setDraft(v: string): void;
  preview(): void;
  cancelPreview(): void;
  save(): Promise<boolean>;
  reload(opts?: { keepDraft: boolean }): Promise<void>;
} {
  const store = writable<ComposeState>({ phase: 'loading', content: '', draft: '', hash: '' });
  let current: ComposeState;
  store.subscribe((s) => (current = s));
  const patch = (p: Partial<ComposeState>) => store.update((s) => ({ ...s, ...p }));

  return {
    state: { subscribe: store.subscribe },

    setDraft(v) {
      if (current.phase === 'loading' || current.phase === 'loadFailed') return;
      // Typing during a save is kept (the save carries a snapshot). Only a finished save goes
      // back to editing; a conflict stays until the user resolves it. The 422 text stays until the next save.
      patch(current.phase === 'saved' ? { draft: v, phase: 'editing' } : { draft: v });
    },

    preview() {
      if (current.phase === 'editing' || current.phase === 'saved') patch({ phase: 'previewing' });
    },

    cancelPreview() {
      if (current.phase === 'previewing') patch({ phase: 'editing' });
    },

    async save() {
      if (current.phase === 'saving' || current.phase === 'loading' || current.phase === 'loadFailed') return false;
      const { draft, hash } = current;
      patch({ phase: 'saving', error: undefined, conflictMessage: undefined, checksPartial: undefined });
      try {
        const res = await deps.save(draft, hash);
        // The file now holds the snapshot that was sent; edits typed meanwhile stay as an unsaved draft.
        patch({
          phase: current.draft === draft ? 'saved' : 'editing',
          content: draft,
          hash: res.hash,
          checksPartial: res.checksPartial === true,
        });
        return true;
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) {
          patch({ phase: 'conflict', conflictMessage: e.message });
        } else {
          patch({ phase: 'editing', error: messageOf(e) });
        }
        return false;
      }
    },

    async reload(opts) {
      const keepDraft = opts?.keepDraft === true;
      if (!keepDraft) patch({ phase: 'loading' });
      try {
        const raw = await deps.load();
        const draft = keepDraft ? current.draft : raw.content;
        patch({
          content: raw.content,
          hash: raw.hash,
          draft,
          error: undefined,
          conflictMessage: undefined,
          // Show what the user would now overwrite: new file content against their draft.
          phase: keepDraft && draft !== raw.content ? 'previewing' : 'editing',
        });
      } catch (e) {
        patch({ error: messageOf(e), phase: keepDraft ? 'conflict' : 'loadFailed' });
      }
    },
  };
}
