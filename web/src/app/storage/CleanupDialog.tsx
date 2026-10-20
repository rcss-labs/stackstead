import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { api, ApiError } from '$lib/api/client';
import type { Operation, StoragePlan, StoragePlanRequest } from '$lib/api/types';
import { reclaimText, sizeText, stateText } from '$lib/storage/storage';
import { refreshRunningOps } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Spinner } from '../components/Spinner';
import { OperationPanel } from '../detail/OperationPanel';
import styles from './CleanupDialog.module.css';

export interface CleanupDialogProps {
  request: StoragePlanRequest;
  onClose: () => void;
  onFinished?: (op: Operation) => void;
}

type PendingFocus = 'field' | 'first-action' | null;

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * Previews a storage prune/remove plan and runs it after the user types the confirmation phrase. The
 * parent mounts it only while a request is set. The execute request is sent only from confirm(), which
 * requires the exact phrase and an unexpired plan.
 */
export function CleanupDialog({ request, onClose, onFinished }: CleanupDialogProps) {
  const uid = useId();
  const [plan, setPlan] = useState<StoragePlan>();
  const [checking, setChecking] = useState(true);
  const [error, setError] = useState('');
  const [fieldError, setFieldError] = useState('');
  const [phrase, setPhrase] = useState('');
  const [now, setNow] = useState(() => Date.now());
  const [expired, setExpired] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [operation, setOperation] = useState<Operation>();
  const [finished, setFinished] = useState(false);

  const generation = useRef(0);
  const mounted = useRef(false);
  const checkStarted = useRef(false);
  const endedOnce = useRef(false);
  const field = useRef<HTMLInputElement>(null);
  const actions = useRef<HTMLDivElement>(null);
  // Where focus goes once the render that shows the new state has committed (the old `await tick()`).
  const pendingFocus = useRef<PendingFocus>(null);
  const onFinishedRef = useRef(onFinished);
  onFinishedRef.current = onFinished;

  const seconds = plan ? Math.max(0, Math.ceil((Date.parse(plan.expiresAt) - now) / 1000)) : 0;
  const stale = expired || (!!plan && seconds === 0);
  // Both lists share the dialog height so the confirm field stays on screen.
  const sharedBudget = !!plan && plan.items.length > 0 && plan.protected.length > 0;
  const volumePlan = plan?.kind === 'volume' || plan?.kind === 'volumes';
  const canClose = !confirming && (!operation || finished);

  function close() {
    if (canClose) onClose();
  }

  // Consumes the queued focus after the commit that shows the new state. After a check the confirm
  // field wins when the plan rendered one; otherwise focus stays inside the dialog on its first action.
  useLayoutEffect(() => {
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    if (field.current && !field.current.disabled) field.current.focus({ preventScroll: true });
    else if (target === 'first-action') actions.current?.querySelector<HTMLElement>('button')?.focus({ preventScroll: true });
  });

  async function check() {
    const mine = ++generation.current;
    setChecking(true);
    setError('');
    setFieldError('');
    setPhrase('');
    setPlan(undefined);
    setExpired(false);
    try {
      const fresh = await api.createStoragePlan(request);
      if (!mounted.current || mine !== generation.current) return;
      setPlan(fresh);
      setNow(Date.now());
    } catch (e) {
      if (mounted.current && mine === generation.current) setError(messageOf(e));
    } finally {
      if (mounted.current && mine === generation.current) {
        // The checking branch's Close button is gone; without a confirm field, keep focus inside the dialog.
        pendingFocus.current = 'first-action';
        setChecking(false);
      }
    }
  }

  // Cleanup only clears `mounted`: StrictMode's mount, cleanup, mount creates one plan whose response
  // still lands, while a real unmount drops the response.
  useEffect(() => {
    mounted.current = true;
    if (!checkStarted.current) {
      checkStarted.current = true;
      void check();
    }
    return () => {
      mounted.current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!plan || operation) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [plan, operation]);

  async function confirm() {
    setNow(Date.now());
    if (!plan || stale || confirming || phrase !== plan.confirmPhrase) return;
    setConfirming(true);
    setError('');
    setFieldError('');
    try {
      const op = await api.confirmStoragePlan(plan.id, phrase);
      if (mounted.current) setOperation(op);
    } catch (e) {
      if (!mounted.current) return;
      if (e instanceof ApiError && e.status === 404) setExpired(true);
      else if (e instanceof ApiError && e.fields.some((f) => f.field === 'confirm')) {
        setFieldError(e.fields.find((f) => f.field === 'confirm')?.message ?? e.message);
        pendingFocus.current = 'field';
      } else setError(messageOf(e));
    } finally {
      if (mounted.current) setConfirming(false);
    }
  }

  function ended(op: Operation) {
    if (endedOnce.current) return;
    endedOnce.current = true;
    setFinished(true);
    toasts.push({
      kind: op.state === 'succeeded' ? 'success' : op.state === 'cancelled' ? 'warning' : 'danger',
      message: `Storage cleanup ${op.state.replaceAll('_', ' ')}`,
    });
    refreshRunningOps();
    onFinishedRef.current?.(op);
  }

  async function cancel(id: string) {
    try {
      await api.cancel(id);
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not cancel: ${messageOf(e)}` });
    }
  }

  return (
    <Dialog open title="Clean up storage" onClose={close}>
      {operation ? (
        <>
          <OperationPanel operationId={operation.id} onCancel={cancel} onFinished={ended} />
          {finished && (
            <div className={styles.actions}>
              <Button variant="secondary" onClick={close}>
                Close
              </Button>
            </div>
          )}
        </>
      ) : checking ? (
        <>
          <p className={styles.cluster}>
            <Spinner /> Checking current state…
          </p>
          <div className={styles.actions}>
            <Button variant="secondary" onClick={close}>
              Close
            </Button>
          </div>
        </>
      ) : (
        <>
          {error && (
            <p role="alert" className={styles.error}>
              {error}
            </p>
          )}
          {plan && (
            <>
              {plan.items.length === 0 ? (
                <p>Nothing to clean</p>
              ) : (
                <>
                  <h3 id={`${uid}-items`}>
                    {plan.mode === 'all-unused' ? 'Expected items' : 'Items to delete'} ({plan.items.length})
                  </h3>
                  <div className={styles.scroll} data-shared={sharedBudget ? 'true' : undefined} role="region" aria-labelledby={`${uid}-items`} tabIndex={0}>
                    <ul>
                      {plan.items.map((item) => (
                        <li key={`${item.kind}:${item.id}`}>
                          <span className={styles.mono}>{item.ref}</span>
                          <span className={`${styles.mono} ${styles.muted}`}>{sizeText(item.bytes)}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                  <p>{reclaimText(plan.reclaim)}</p>
                </>
              )}
              {plan.protected.length > 0 && (
                <>
                  <h3 id={`${uid}-protected`}>Protected ({plan.protected.length})</h3>
                  <div className={styles.scroll} data-shared={sharedBudget ? 'true' : undefined} role="region" aria-labelledby={`${uid}-protected`} tabIndex={0}>
                    <ul>
                      {plan.protected.map((item) => (
                        <li key={`${item.kind}:${item.id}`} className={styles.protected}>
                          <span className={styles.mono}>{item.ref}</span>
                          <span>
                            {item.reason}
                            {item.stack && (
                              <>
                                {' · '}
                                <Link to={`/stacks/${encodeURIComponent(item.stack)}`}>{item.stack}</Link> ({stateText(item.stackState)})
                              </>
                            )}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                </>
              )}
              {plan.warnings.map((warning) => (
                <p key={warning} className={styles.warning}>
                  {warning}
                </p>
              ))}
              {plan.items.length > 0 && (
                <>
                  {volumePlan && <p className={styles.warning}>Volumes hold data. This cannot be undone.</p>}
                  <p className={styles.muted}>
                    Expires in {Math.floor(seconds / 60)}:{String(seconds % 60).padStart(2, '0')}
                  </p>
                  {stale ? (
                    <p>This plan has expired. Re-check current state before deleting.</p>
                  ) : (
                    <>
                      <p id={`${uid}-phrase`}>
                        <code>{plan.confirmPhrase}</code>
                        {plan.mode === 'all-unused' && (
                          <>
                            {' '}
                            <span className={styles.warning}>not exact</span>
                          </>
                        )}
                      </p>
                      <label htmlFor={`${uid}-confirm`}>Type {plan.confirmPhrase} to confirm</label>
                      <input
                        id={`${uid}-confirm`}
                        className={styles.mono}
                        ref={field}
                        value={phrase}
                        onChange={(event) => setPhrase(event.target.value)}
                        disabled={confirming}
                        aria-describedby={`${uid}-phrase${fieldError ? ` ${uid}-error` : ''}`}
                        aria-invalid={fieldError ? 'true' : undefined}
                        autoComplete="off"
                        spellCheck={false}
                      />
                      {fieldError && (
                        <p id={`${uid}-error`} className={styles.error} role="alert">
                          {fieldError}
                        </p>
                      )}
                    </>
                  )}
                </>
              )}
            </>
          )}
          <div className={styles.actions} ref={actions}>
            <Button variant="secondary" disabled={confirming} onClick={close}>
              Close
            </Button>
            {plan && plan.items.length > 0 &&
              (stale ? (
                <Button variant="secondary" onClick={() => void check()}>
                  Re-check
                </Button>
              ) : (
                <Button variant="danger" confirm disabled={phrase !== plan.confirmPhrase || confirming} loading={confirming} onClick={() => void confirm()}>
                  Delete
                </Button>
              ))}
          </div>
        </>
      )}
    </Dialog>
  );
}
