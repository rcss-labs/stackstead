import { useEffect, useId, useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { StackDetail } from '$lib/api/types';
import type { ServiceForm as Form } from '$lib/compose/form';
import { createServiceAdd } from '$lib/stacks/serviceAdd';
import { get } from '$lib/store';
import { refreshStatus } from '$lib/stores/live';
import { refreshStacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { useStore } from '../hooks/useStore';
import { DiffView } from '../stacks/DiffView';
import { FileBrowser } from '../stacks/FileBrowser';
import { NetworkPicker } from '../stacks/NetworkPicker';
import { ServiceForm, type ServiceFormHandle } from '../stacks/ServiceForm';
import styles from './AddServicePanel.module.css';

export interface AddServicePanelProps {
  stack: StackDetail;
  /** Called with the refreshed detail and the reviewed service name after the add (never after the panel unmounted). */
  onAdded: (detail: StackDetail, checksPartial: boolean, service: string) => void;
  onCancel: () => void;
  /** Re-fetch the stack after a conflict. */
  onReload: () => Promise<StackDetail>;
  /** True while the name or form differs from an empty one. */
  onDirtyChange?: (dirty: boolean) => void;
}

const cloneForm = (f: Form): Form => ({ ...f, readOnly: new Set(f.readOnly) });
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

function unavailableRefresh(e: unknown): never {
  if (e instanceof ApiError && e.status === 503) void refreshStatus();
  throw e;
}

export function AddServicePanel({ stack, onAdded, onCancel, onReload, onDirtyChange }: AddServicePanelProps) {
  const stackName = stack.name;
  const reloadRef = useRef(onReload);
  reloadRef.current = onReload;
  const addedRef = useRef(onAdded);
  addedRef.current = onAdded;
  const savedService = useRef('');

  const [add] = useState(() =>
    createServiceAdd(stack.hash, {
      preview: (service, config, hash) => api.previewAddService(stackName, service, config, hash).catch(unavailableRefresh),
      add: async (service, config, hash) => {
        savedService.current = service;
        const res = await api.addService(stackName, service, config, hash).catch(unavailableRefresh);
        // The server accepted the write even if this panel has since been closed.
        void refreshStacks();
        return { detail: res.data, checksPartial: res.checksPartial };
      },
      compose: () => api.compose(stackName).catch(unavailableRefresh),
      reloadDetail: () => reloadRef.current(),
    }),
  );
  const form = useStore(add.form);
  const name = useStore(add.name);
  const errors = useStore(add.errors);
  const phase = useStore(add.phase);
  const notice = useStore(add.notice);
  const conflictMessage = useStore(add.conflictMessage);
  const previewText = useStore(add.previewText);
  const isDirty = useStore(add.dirty);

  // Unmounting ends the session. The machine is disposed on a timer so a StrictMode remount keeps it.
  const mounted = useRef(false);
  const pendingDispose = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    clearTimeout(pendingDispose.current);
    mounted.current = true;
    return () => {
      mounted.current = false;
      pendingDispose.current = setTimeout(() => add.dispose(), 0);
    };
  }, [add]);

  const [reloading, setReloading] = useState(false);
  const [browse, setBrowse] = useState<{ mode: 'dir' | 'file'; cb: (path: string) => void } | undefined>();
  const root = useRef<HTMLDivElement>(null);
  const headingId = useId();
  const serviceForm = useRef<ServiceFormHandle>(null);
  const previewHeading = useRef<HTMLHeadingElement>(null);
  const [focusRequest, setFocusRequest] = useState(0);
  const focusCounter = useRef(0);

  const change = (next: Form) => add.form.set(cloneForm(next));

  const dirtyCallback = useRef(onDirtyChange);
  dirtyCallback.current = onDirtyChange;
  useEffect(() => {
    dirtyCallback.current?.(isDirty);
  }, [isDirty]);

  useEffect(() => {
    if (!isDirty) return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [isDirty]);

  const firstField = () => root.current?.querySelector<HTMLElement>('input, select, textarea');
  // "+ Add" was just clicked: keep keyboard focus in the panel.
  useEffect(() => {
    firstField()?.focus();
  }, []);

  useEffect(() => {
    if (!focusRequest) return;
    if (get(add.phase) === 'previewing') previewHeading.current?.focus();
    else if (Object.keys(get(add.errors)).length > 0) serviceForm.current?.focusFirstError();
  }, [focusRequest, add]);
  const requestFocus = () => setFocusRequest(++focusCounter.current);

  async function preview() {
    await add.preview();
    if (mounted.current) requestFocus();
  }

  async function save() {
    const detail = await add.save();
    if (!mounted.current || !detail) return;
    toasts.push({ kind: 'success', message: `Added ${savedService.current} to compose.yaml. Run Up to start it.` });
    const partial = get(add.checksPartial);
    if (partial) toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
    addedRef.current(detail, partial, savedService.current);
  }

  async function reload(keep: boolean) {
    if (reloading) return;
    setReloading(true);
    try {
      const detail = await reloadRef.current();
      if (!mounted.current) return;
      if (keep) {
        add.reload(detail);
        await preview();
      } else {
        add.discard(detail);
      }
    } catch (e) {
      if (mounted.current) {
        if (e instanceof ApiError && e.status === 503) void refreshStatus();
        toasts.push({ kind: 'danger', message: `Could not reload the stack: ${messageOf(e)}` });
      }
    } finally {
      if (mounted.current) setReloading(false);
    }
  }

  const reviewing = phase === 'previewing' || phase === 'saving';

  return (
    <div className={styles.panel} ref={root} role="region" aria-labelledby={headingId}>
      <h2 id={headingId} className="visually-hidden">
        Add service
      </h2>
      {notice && (
        <Banner kind="danger" title="Could not complete the request">
          <span className={styles.msg}>{notice.message}</span>
        </Banner>
      )}

      {phase === 'conflict' && (
        <>
          <Banner kind="danger" title="Save refused">
            <span className={styles.msg}>{conflictMessage}</span>
          </Banner>
          <div className={styles.row}>
            <Button variant="primary" loading={reloading} onClick={() => void reload(true)}>
              Reload
            </Button>
            <Button variant="secondary" disabled={reloading} onClick={() => void reload(false)}>
              Discard my changes
            </Button>
          </div>
        </>
      )}

      {reviewing && (
        <>
          <h3 tabIndex={-1} ref={previewHeading} className={styles.heading}>
            Review the service to add
          </h3>
          {previewText && <DiffView original={previewText.original} modified={previewText.modified} ariaLabel="Changes to compose.yaml" />}
        </>
      )}

      {/* Kept mounted (hidden) while previewing so focus, scroll and the values survive. */}
      <div className={styles.formCard} hidden={reviewing} inert={phase === 'busy' || phase === 'saving' || reloading}>
        <ServiceForm
          ref={serviceForm}
          form={form}
          onChange={change}
          showName
          name={name}
          onNameChange={(value) => add.name.set(value)}
          errors={errors}
          idPrefix="svc-add"
          onBrowse={(mode, cb) => setBrowse({ mode, cb })}
          networkPicker={(f, rowErrors) => (
            <NetworkPicker idPrefix="svc-add-networks" value={f.networks} onChange={(networks) => change({ ...get(add.form), networks })} errors={rowErrors} />
          )}
        />
      </div>

      <div className={styles.row}>
        {reviewing ? (
          <>
            <Button variant="secondary" disabled={phase === 'saving'} onClick={() => add.cancelPreview()}>
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
            disabledReason={phase === 'conflict' ? 'Resolve the conflict first (Reload or Discard)' : undefined}
            onClick={() => void preview()}
          >
            Preview changes
          </Button>
        )}
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
      </div>

      <FileBrowser
        open={browse !== undefined}
        mode={browse?.mode ?? 'dir'}
        title={browse?.mode === 'file' ? 'Choose a Dockerfile' : 'Choose a build context'}
        onPick={(path) => {
          browse?.cb(path);
          setBrowse(undefined);
        }}
        onClose={() => setBrowse(undefined)}
      />
    </div>
  );
}
