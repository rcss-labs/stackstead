import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useNavigate } from 'react-router';
import type { DockerfileView } from '$lib/api/types';
import { DOCKERFILE_TEMPLATE } from '$lib/compose/form';
import { createComposeEditor } from '$lib/stacks/compose';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { useServer } from '../servers/ServerContext';
import { useStore } from '../hooks/useStore';
import { DiffView } from '../stacks/DiffView';
import { YamlEditor } from '../stacks/YamlEditor';
import styles from './DockerfilePanel.module.css';

const OUTSIDE_REASONS = ['outside the stack folder', 'outside the allowed directories'];
export const OUTSIDE_NOTE = "This Dockerfile is outside the stack's folder and can't be edited here.";
const CONFLICT_REASON = 'Resolve the conflict first (Reload or Discard)';

/** A service's Dockerfile on the stack page: edit, save, or save and rebuild just that service. */
export function DockerfilePanel({ stackName, service, onDirtyChange }: { stackName: string; service: string; onDirtyChange?: (dirty: boolean) => void }) {
  const { api, href, trackOperation, refreshRunningOps } = useServer();
  const navigate = useNavigate();
  const [view, setView] = useState<DockerfileView | undefined>();
  const viewRef = useRef<DockerfileView | undefined>(undefined);
  const [rebuilding, setRebuilding] = useState(false);
  const [editor] = useState(() =>
    createComposeEditor(stackName, {
      load: async () => {
        const v = await api.dockerfile(stackName, service);
        viewRef.current = v;
        setView(v);
        return { content: v.content, hash: v.hash };
      },
      save: async (content, hash) => ({ hash: (await api.saveDockerfile(stackName, service, content, hash)).hash }),
    }),
  );
  const st = useStore(editor.state);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    void editor.reload().then(() => {
      // A Dockerfile that does not exist yet starts from a template; saving it creates the file.
      const loaded = get(editor.state);
      if (mounted.current && loaded.phase === 'editing' && loaded.hash === '' && loaded.content === '' && viewRef.current?.editable && viewRef.current.exists === false) {
        editor.setDraft(DOCKERFILE_TEMPLATE);
      }
    });
  }, [editor]);

  const isDirty = st.draft !== st.content;
  // The untouched template of a new file is the baseline, not an edit the user would lose.
  const untouchedTemplate = view?.exists === false && st.content === '' && st.draft === DOCKERFILE_TEMPLATE;
  const unsaved = isDirty && !untouchedTemplate;
  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => {
    dirtyCallback.current?.(unsaved);
  }, [unsaved]);
  useEffect(() => () => dirtyCallback.current?.(false), []);
  const previewing = st.phase === 'previewing';
  const loading = st.phase === 'loading' || st.phase === 'loadFailed';
  const busy = st.phase === 'saving' || rebuilding;
  const editable = view?.editable === true;
  const conflict = st.phase === 'conflict';
  const hideEditor = !editable && st.content === '';

  async function save(): Promise<boolean> {
    if (!isDirty || conflict || busy || loading) return false;
    const ok = await editor.save();
    if (ok) toasts.push({ kind: 'success', message: 'Dockerfile saved' });
    return ok;
  }

  async function saveAndRebuild() {
    if (conflict || busy || loading) return;
    const wrote = isDirty;
    if (wrote && !(await save())) return;
    setRebuilding(true);
    try {
      const op = await api.upService(stackName, service);
      trackOperation(op);
      void refreshRunningOps();
      if (mounted.current) void navigate(href(`/stacks/${encodeURIComponent(stackName)}?tab=operations&op=${encodeURIComponent(op.id)}`));
    } catch (e) {
      toasts.push({ kind: 'danger', message: `${wrote ? 'Saved, but r' : 'R'}ebuilding ${service} failed: ${e instanceof Error ? e.message : String(e)}` });
    } finally {
      if (mounted.current) setRebuilding(false);
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if (editable && (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    }
  }

  const saveReason = !isDirty ? 'No changes to save' : conflict ? CONFLICT_REASON : undefined;
  const label = `Dockerfile for ${service}`;

  return (
    <div className={styles.panel} onKeyDown={onKeyDown}>
      {st.phase === 'loading' && <p role="status">Loading Dockerfile…</p>}
      {st.phase === 'loadFailed' && (
        <>
          <Banner kind="danger" title="Could not load the Dockerfile">
            {st.error}
          </Banner>
          <div>
            <Button variant="secondary" onClick={() => void editor.reload()}>
              Retry
            </Button>
          </div>
        </>
      )}

      {conflict && (
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
        </>
      )}

      {!loading && view && (
        <div className={styles.card}>
          {!editable && (
            <Banner kind="info" title="Read-only">
              {OUTSIDE_REASONS.includes(view.reason ?? '') ? OUTSIDE_NOTE : `This Dockerfile can't be edited here: ${view.reason ?? 'not editable'}`}
              {view.reason === 'outside the allowed directories' && <> Its content isn't shown because it's outside the allowed directories.</>}
            </Banner>
          )}
          <div className={styles.toolbar}>
            <span className={styles.file}>{view.path}</span>
            {editable && isDirty && <span className={styles.modified}>· modified</span>}
            <span className={styles.spacer} />
            {editable && (
              <>
                <span className={styles.shortcut}>⌘S / Ctrl+S to save</span>
                {previewing ? (
                  <Button variant="secondary" size="sm" onClick={() => editor.cancelPreview()}>
                    Back to editing
                  </Button>
                ) : (
                  <Button variant="secondary" size="sm" disabledReason={!isDirty ? 'No changes to preview' : conflict ? CONFLICT_REASON : undefined} onClick={() => editor.preview()}>
                    Preview changes
                  </Button>
                )}
                <Button variant="secondary" size="sm" loading={st.phase === 'saving'} disabledReason={saveReason} onClick={() => void save()}>
                  Save
                </Button>
                <Button variant="primary" size="sm" loading={rebuilding} disabledReason={conflict ? CONFLICT_REASON : undefined} onClick={() => void saveAndRebuild()}>
                  Save & rebuild
                </Button>
              </>
            )}
          </div>

          {previewing && (
            <>
              <p className={styles.hint}>Showing the file as it is now against your draft.</p>
              <DiffView original={st.content} modified={st.draft} ariaLabel={`Changes to ${label}`} language="dockerfile" />
            </>
          )}

          <div hidden={previewing || hideEditor}>
            <YamlEditor
              value={st.draft}
              original={editable ? st.content : undefined}
              readonly={!editable}
              language="dockerfile"
              ariaLabel={label}
              onChange={(v) => editor.setDraft(v)}
            />
          </div>

          {editable && st.phase === 'editing' && st.error && <pre className={`${styles.msg} ${styles.error}`}>{st.error}</pre>}
          {editable && <p className={styles.footer}>Save & rebuild rebuilds the image and recreates only this service.</p>}
        </div>
      )}
    </div>
  );
}
