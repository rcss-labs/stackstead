import { useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { ApiError } from '$lib/api/client';
import type { Operation, ServiceDetail, StackDetail } from '$lib/api/types';
import { defaultImageTag, type ServiceForm as Form } from '$lib/compose/form';
import { ownerIsViewed } from '$lib/images/library';
import { parseLibraryRef } from '$lib/images/ref';
import { dockerfileNote } from '$lib/stacks/dockerfileNote';
import { createServiceEdit } from '$lib/stacks/serviceEdit';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { useStore } from '../hooks/useStore';
import { DiffView } from '../stacks/DiffView';
import { FileBrowser } from '../stacks/FileBrowser';
import { NetworkPicker } from '../stacks/NetworkPicker';
import { BROWSE_TITLES, ServiceForm, type BrowsePurpose, type ServiceFormHandle } from '../stacks/ServiceForm';
import { OperationPanel } from './OperationPanel';
import styles from './ServiceEditor.module.css';

import { useServer } from '../servers/ServerContext';
export interface ServiceEditorProps {
  stack: StackDetail;
  service: ServiceDetail;
  /** Called with the new compose hash after a successful save. */
  onSaved?: (hash: string) => void;
  /** Called when Apply has started an operation (the panel is shown here). */
  onApplied?: (op: Operation) => void;
  /** Send the user to the raw YAML editor. The page asks first when the form is dirty, and the draft is discarded. */
  onRawEditor: () => void;
  /**
   * Re-fetch the stack after a conflict or when the file moved on. The parent keys this editor by stack and
   * service and must NOT remount it on reload: it just returns the fresh detail, and the editor lays the
   * user's changed groups on top of it.
   */
  onReload: () => Promise<StackDetail>;
  /** Why Apply (compose up) is unavailable, if it is. */
  lifecycleDisabledReason?: string;
  /** True while the form differs from the saved config. */
  onDirtyChange?: (dirty: boolean) => void;
  /** Rendered in a footer under the editor (the page puts Remove service here). */
  removeSlot?: ReactNode;
}

type FocusRequest = { kind: 'preview' | 'save' | 'first'; n: number };

const cloneForm = (f: Form): Form => ({ ...f, readOnly: new Set(f.readOnly) });
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function ServiceEditor({ stack, service, onSaved, onApplied, onRawEditor, onReload, lifecycleDisabledReason, onDirtyChange, removeSlot }: ServiceEditorProps) {
  // The page keys this editor by stack and service; both are fixed for its lifetime.
  const { api, id: serverId, refreshStatus, trackOperation, refreshRunningOps, refreshStacks } = useServer();
  const stackName = stack.name;
  const serviceName = service.name;
  const reloadRef = useRef(onReload);
  reloadRef.current = onReload;

  const [edit] = useState(() =>
    createServiceEdit(
      serviceName,
      { config: service.config, readOnly: service.readOnly, hash: stack.hash },
      {
        // A 503 means Docker went away: have the status banners catch up.
        preview: (config, hash) => api.previewService(stackName, serviceName, config, hash).catch((e) => unavailableRefresh(e, refreshStatus)),
        update: async (config, hash, dockerfile) => {
          const res = await api.updateService(stackName, serviceName, config, hash, dockerfile).catch((e) => unavailableRefresh(e, refreshStatus));
          return { hash: res.data.hash, checksPartial: res.checksPartial };
        },
        compose: () => api.compose(stackName),
        loadDockerfile: () => api.dockerfile(stackName, serviceName),
        reloadDetail: () => reloadRef.current(),
      },
    ),
  );
  const errors = useStore(edit.errors);
  const phase = useStore(edit.phase);
  const notice = useStore(edit.notice);
  const conflictMessage = useStore(edit.conflictMessage);
  const previewText = useStore(edit.previewText);
  const rebased = useStore(edit.rebased);
  const discardedGroups = useStore(edit.discardedGroups);
  const formVersion = useStore(edit.formVersion);
  const isDirty = useStore(edit.dirty);
  const dockerfileView = useStore(edit.dockerfileView);
  const dockerfileConflict = useStore(edit.dockerfileConflict);
  // A Dockerfile the form cannot edit leaves the build form as it is; say why next to the Source group.
  const notEditableNote = dockerfileView && !dockerfileView.editable ? dockerfileNote(dockerfileView.reason) : undefined;

  // The form is controlled here and pushed to the state machine as a copy on every change.
  const [form, setForm] = useState<Form>(() => cloneForm(get(edit.form)));
  const formRef = useRef(form);
  function change(next: Form) {
    formRef.current = next;
    setForm(next);
    edit.form.set(cloneForm(next));
  }
  // The machine replaces the form itself on rebase and discard: follow it (adjusted while rendering).
  const [seenVersion, setSeenVersion] = useState(formVersion);
  if (seenVersion !== formVersion) {
    setSeenVersion(formVersion);
    const fresh = cloneForm(get(edit.form));
    formRef.current = fresh;
    setForm(fresh);
  }

  const root = useRef<HTMLDivElement>(null);
  const serviceForm = useRef<ServiceFormHandle>(null);
  const previewHeading = useRef<HTMLHeadingElement>(null);
  const conflictBox = useRef<HTMLDivElement>(null);
  const savedHeading = useRef<HTMLHeadingElement>(null);
  const [browse, setBrowse] = useState<{ mode: 'dir' | 'file'; purpose: BrowsePurpose; cb: (path: string) => void } | undefined>();
  const [operationId, setOperationId] = useState<string | undefined>();
  const [applying, setApplying] = useState(false);
  const [operationRunning, setOperationRunning] = useState(false);
  const [focusRequest, setFocusRequest] = useState<FocusRequest | undefined>();
  const focusCounter = useRef(0);
  const requestFocus = (kind: FocusRequest['kind']) => setFocusRequest({ kind, n: ++focusCounter.current });

  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => {
    dirtyCallback.current?.(isDirty);
  }, [isDirty]);

  useEffect(() => {
    if (!isDirty || phase === 'saved') return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [isDirty, phase]);

  const firstField = () => root.current?.querySelector<HTMLElement>('input, select, textarea');
  // Edit was just clicked and its button is gone: keep focus in the editor.
  useEffect(() => {
    firstField()?.focus();
  }, []);

  // Focus moves run after the render that shows the new phase.
  useEffect(() => {
    if (!focusRequest) return;
    const now = get(edit.phase);
    if (focusRequest.kind === 'first') {
      firstField()?.focus();
    } else if (now === 'previewing') {
      previewHeading.current?.focus();
    } else if (now === 'saved') {
      savedHeading.current?.focus();
    } else if (now === 'conflict') {
      conflictBox.current?.focus();
    } else if (Object.keys(get(edit.errors)).length > 0 || get(edit.notice)) {
      serviceForm.current?.focusFirstError();
      if (!document.activeElement || document.activeElement === document.body) firstField()?.focus();
    }
  }, [focusRequest, edit]);

  async function preview() {
    await edit.preview();
    requestFocus('preview');
  }

  async function save() {
    const ok = await edit.save();
    if (ok) {
      toasts.push({ kind: 'success', message: `Saved ${serviceName}. Apply to run the change.` });
      if (get(edit.checksPartial)) toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
      onSaved?.(get(edit.hash));
      const ref = formRef.current.source === 'library' ? formRef.current.library?.ref : undefined;
      if (ref) void fetchPeerImage(ref);
    }
    requestFocus('save');
  }

  /** A peer-owned library version has to be on this server before Apply; started alongside the save, never blocking it. */
  async function fetchPeerImage(ref: string) {
    const parsed = parseLibraryRef(ref);
    if (!parsed) return;
    try {
      const library = await api.images();
      const owned = library.images.find((i) => i.slug === parsed.slug && i.owner.id.slice(0, 12) === parsed.owner);
      if (!owned || ownerIsViewed(owned, serverId)) return;
      const op = await api.fetchImage(ref);
      trackOperation({ id: op.id, stack: op.stack, action: op.action });
      void refreshRunningOps();
    } catch (e) {
      const message = e instanceof ApiError && e.code === 'arch_mismatch' ? 'that version was built for a different architecture than this server' : messageOf(e);
      toasts.push({ kind: 'danger', message: `Saved, but could not fetch ${parsed.slug}:${parsed.tag}: ${message}` });
    }
  }

  function backToEditing() {
    edit.cancelPreview();
    requestFocus('first');
  }

  async function reload(keep: boolean) {
    let detail: StackDetail;
    try {
      detail = await reloadRef.current();
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not reload the stack: ${messageOf(e)}` });
      return;
    }
    if (keep) {
      await edit.reload(detail);
      await preview(); // show the user's values against the new file
    } else {
      await edit.discard(detail);
      requestFocus('first');
    }
  }

  async function apply() {
    if (applying || operationRunning || lifecycleDisabledReason) return;
    setApplying(true);
    try {
      const op = await api.act(stackName, 'up');
      trackOperation({ id: op.id, stack: stackName, action: 'up' });
      setOperationId(op.id);
      setOperationRunning(true);
      onApplied?.(op);
      void refreshRunningOps();
    } catch (e) {
      const msg = e instanceof ApiError || e instanceof Error ? e.message : String(e);
      toasts.push({ kind: 'danger', message: `Could not start apply: ${msg}` });
    } finally {
      setApplying(false);
    }
  }

  async function cancelOperation(id: string) {
    try {
      await api.cancel(id);
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not cancel: ${messageOf(e)}` });
    }
  }

  function finished() {
    setOperationRunning(false);
    void refreshStacks();
    refreshRunningOps();
  }

  const announcement =
    phase === 'previewing'
      ? 'Preview ready. Review the changes, then save.'
      : phase === 'saving'
        ? 'Saving…'
        : phase === 'saved'
          ? 'Saved. You can apply the change now.'
          : phase === 'conflict'
            ? 'Save refused: the file changed.'
            : '';
  const reviewing = phase === 'previewing' || phase === 'saving';

  return (
    <div className={styles.editor} ref={root}>
      <div className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </div>

      {notice && (
        <Banner kind="danger" title={notice.kind === 'rawEditor' ? 'The form cannot make this change' : 'Could not complete the request'}>
          <span className={styles.msg}>{notice.message}</span>
          {notice.kind === 'rawEditor' && (
            <div className={styles.row}>
              <Button variant="secondary" onClick={onRawEditor}>
                Open raw YAML editor
              </Button>
            </div>
          )}
        </Banner>
      )}

      {phase === 'conflict' && (
        <div ref={conflictBox} tabIndex={-1}>
          <Banner kind="danger" title="Save refused">
            <span className={styles.msg}>{conflictMessage}</span>
            {dockerfileConflict && <span className={styles.msg}> The Dockerfile changed on disk; your text will replace it.</span>}
          </Banner>
          <div className={styles.row}>
            <Button variant="primary" onClick={() => void reload(true)}>
              Reload
            </Button>
            <Button variant="secondary" onClick={() => void reload(false)}>
              Discard my changes
            </Button>
          </div>
        </div>
      )}

      {reviewing && (
        <section className={styles.section}>
          <h3 tabIndex={-1} ref={previewHeading} className={styles.heading}>
            Review changes to {serviceName}
          </h3>
          {rebased && (
            <Banner kind="warning" title="The file changed since you opened this editor">
              Your changes were applied on top of the new version; review the diff.
              {discardedGroups.length > 0 &&
                ` Your changes to ${discardedGroups.join(', ')} were discarded because that part of the file can now only be edited as raw YAML.`}
            </Banner>
          )}
          <p className={styles.hint}>The compose file as it is now, against the file this edit would write.</p>
          {previewText && <DiffView original={previewText.original} modified={previewText.modified} ariaLabel="Changes to compose.yaml" />}
        </section>
      )}

      {/* Kept mounted (hidden) while previewing so focus, scroll and the form values survive. */}
      <div className={styles.formCard} hidden={reviewing} inert={phase === 'busy'}>
        <ServiceForm
          ref={serviceForm}
          form={form}
          onChange={change}
          errors={errors}
          idPrefix="svc"
          name={serviceName}
          sourceNote={notEditableNote}
          onSourceChange={(source) => {
            const current = formRef.current;
            // Building replaces the image, so a pulled image's name must not become the built image's tag.
            if (source !== 'dockerfile' || (current.source !== 'image' && current.image.trim() !== '')) return;
            return { ...cloneForm(current), image: defaultImageTag(stackName, serviceName) };
          }}
          onBrowse={(mode, cb, purpose) => setBrowse({ mode, purpose, cb })}
          onRawEditor={onRawEditor}
          networkPicker={(f, rowErrors) => (
            <NetworkPicker idPrefix="svc-networks" value={f.networks} onChange={(networks) => change({ ...formRef.current, networks })} errors={rowErrors} />
          )}
        />
      </div>

      <div className={styles.row}>
        {reviewing ? (
          <>
            <Button variant="secondary" disabled={phase === 'saving'} onClick={backToEditing}>
              Back to editing
            </Button>
            <Button variant="primary" loading={phase === 'saving'} onClick={() => void save()}>
              Save
            </Button>
          </>
        ) : (
          <Button
            variant="primary"
            loading={phase === 'busy'}
            disabledReason={phase === 'conflict' ? 'Resolve the conflict first (Reload or Discard)' : !isDirty ? 'No changes to preview' : undefined}
            onClick={() => void preview()}
          >
            Preview changes
          </Button>
        )}
      </div>

      {phase === 'saved' && (
        <section className={styles.section} aria-labelledby="svc-saved-title">
          <h3 id="svc-saved-title" tabIndex={-1} ref={savedHeading} className={styles.heading}>
            Saved
          </h3>
          <p className={styles.hint}>
            The file is updated. Apply runs <code>compose up</code> so the containers match it.
          </p>
          <div>
            <Button
              variant="primary"
              loading={applying}
              disabledReason={lifecycleDisabledReason ?? (operationRunning ? 'An operation is already running' : undefined)}
              onClick={() => void apply()}
            >
              Apply
            </Button>
          </div>
        </section>
      )}

      {operationId && (
        <section className={styles.section}>
          <OperationPanel key={operationId} operationId={operationId} onCancel={cancelOperation} onFinished={finished} />
        </section>
      )}

      {removeSlot && <footer className={styles.footer}>{removeSlot}</footer>}

      <FileBrowser
        open={browse !== undefined}
        mode={browse?.mode ?? 'dir'}
        title={BROWSE_TITLES[browse?.purpose ?? 'context']}
        onPick={(path) => {
          browse?.cb(path);
          setBrowse(undefined);
        }}
        onClose={() => setBrowse(undefined)}
      />
    </div>
  );
}

function unavailableRefresh(e: unknown, refreshStatus: () => Promise<void>): never {
  if (e instanceof ApiError && e.status === 503) void refreshStatus();
  throw e;
}
