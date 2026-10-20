import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { api } from '$lib/api/client';
import { createComposeEditor } from '$lib/stacks/compose';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { useStore } from '../hooks/useStore';
import { DiffView } from '../stacks/DiffView';
import { YamlEditor, type YamlEditorHandle } from '../stacks/YamlEditor';
import styles from './ComposeEditor.module.css';

export interface ComposeEditorProps {
  stackName: string;
  parseError?: string;
  /** Path of the compose file, used only to name the file and its backup. */
  composePath?: string;
  onSaved?: (hash: string) => void;
  onApply?: () => void;
  /** True while the draft differs from the saved file. */
  onDirtyChange?: (dirty: boolean) => void;
  /** Move keyboard focus into the editor once the file has loaded (set when the user asked to edit). */
  autoFocus?: boolean;
}

const CONFLICT_REASON = 'Resolve the conflict first (Reload or Discard)';

export function ComposeEditor({ stackName, parseError, composePath, onSaved, onApply, onDirtyChange, autoFocus }: ComposeEditorProps) {
  // The screen keys this component by stack, so the name is fixed for its lifetime.
  const [editor] = useState(() =>
    createComposeEditor(stackName, {
      load: () => api.compose(stackName),
      save: async (content, hash) => {
        const res = await api.saveCompose(stackName, content, hash);
        return { hash: res.data.hash, checksPartial: res.checksPartial };
      },
    }),
  );
  const st = useStore(editor.state);
  const yaml = useRef<YamlEditorHandle>(null);
  const isDirty = st.draft !== st.content;
  const previewing = st.phase === 'previewing';
  const baseName = composePath?.split('/').filter(Boolean).pop();
  const fileName = baseName ?? 'compose.yaml';
  const backupName = baseName ? `${baseName}.bak` : 'a .bak file next to it';

  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => {
    dirtyCallback.current?.(isDirty);
  }, [isDirty]);

  const focused = useRef(false);
  const ready = st.phase !== 'loading' && st.phase !== 'loadFailed';
  useEffect(() => {
    if (!autoFocus || !ready || focused.current) return;
    focused.current = true;
    yaml.current?.focus();
  }, [autoFocus, ready]);

  useEffect(() => {
    void editor.reload();
  }, [editor]);

  useEffect(() => {
    if (!isDirty) return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = ''; // legacy browsers only show the prompt when this is set
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [isDirty]);

  async function save() {
    if (!isDirty || st.phase === 'conflict' || st.phase === 'saving' || st.phase === 'loading') return;
    const ok = await editor.save();
    const after = get(editor.state);
    if (ok) {
      toasts.push({ kind: 'success', message: `Saved — previous version kept as ${backupName}` });
      if (after.checksPartial) toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
      onSaved?.(after.hash);
    } else if (after.phase === 'editing' && after.error) {
      // Best effort: compose usually names the failing line.
      const m = /line (\d+)/.exec(after.error);
      if (m) queueMicrotask(() => yaml.current?.goToLine(Number(m[1])));
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    }
  }

  const reason = !isDirty ? 'No changes to save' : st.phase === 'conflict' ? CONFLICT_REASON : undefined;

  return (
    <div className={styles.compose} onKeyDown={onKeyDown}>
      {parseError && (
        <Banner kind="danger" title="This file cannot be edited with forms">
          Only the raw editor is available. <pre className={styles.msg}>{parseError}</pre>
        </Banner>
      )}

      {st.phase === 'loading' && <p role="status">Loading compose file…</p>}
      {st.phase === 'loadFailed' && (
        <>
          <Banner kind="danger" title="Could not load the compose file">
            {st.error}
          </Banner>
          <div>
            <Button variant="secondary" onClick={() => void editor.reload()}>
              Retry
            </Button>
          </div>
        </>
      )}

      {st.phase === 'conflict' && (
        <>
          <Banner kind="danger" title="Save refused">
            <span className={styles.msgText}>{st.conflictMessage}</span>
          </Banner>
          <div className={styles.row}>
            <Button variant="primary" onClick={() => void editor.reload({ keepDraft: true })}>
              Reload
            </Button>
            <Button variant="secondary" onClick={() => void editor.reload({ keepDraft: false })}>
              Discard my changes
            </Button>
          </div>
          {st.error && <pre className={styles.msg}>{st.error}</pre>}
        </>
      )}

      {st.phase !== 'loading' && st.phase !== 'loadFailed' && (
        <div className={styles.card}>
          <div className={styles.toolbar}>
            <span className={styles.file}>{fileName}</span>
            {isDirty && <span className={styles.modified}>· modified</span>}
            <span className={styles.spacer} />
            <span className={styles.shortcut}>⌘S / Ctrl+S to save</span>
            {previewing ? (
              <Button variant="secondary" size="sm" onClick={() => editor.cancelPreview()}>
                Back to editing
              </Button>
            ) : (
              <Button
                variant="secondary"
                size="sm"
                disabledReason={!isDirty ? 'No changes to preview' : st.phase === 'conflict' ? CONFLICT_REASON : undefined}
                onClick={() => editor.preview()}
              >
                Preview changes
              </Button>
            )}
            <Button variant="primary" size="sm" loading={st.phase === 'saving'} disabledReason={reason} onClick={() => void save()}>
              Save
            </Button>
            {st.phase === 'saved' && (
              <Button variant="secondary" size="sm" onClick={() => onApply?.()}>
                Apply
              </Button>
            )}
          </div>

          {previewing && (
            <>
              <p className={styles.hint}>Showing the file as it is now against your draft.</p>
              <DiffView original={st.content} modified={st.draft} ariaLabel="Changes to compose.yaml" />
            </>
          )}

          {/* Kept mounted (hidden) during preview so the editor and its undo history survive. */}
          <div hidden={previewing}>
            <YamlEditor
              ref={yaml}
              value={st.draft}
              original={st.content}
              ariaLabel="Compose file"
              onChange={(v) => editor.setDraft(v)}
            />
          </div>

          {st.phase === 'editing' && st.error && (
            <pre className={`${styles.msg} ${styles.error}`} data-testid="compose-error">
              {st.error}
            </pre>
          )}

          <p className={styles.footer}>
            Saving keeps the previous version as <code>{fileName}.bak</code>. Comments and key order are preserved.
          </p>
        </div>
      )}
    </div>
  );
}
