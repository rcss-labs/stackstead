import { useEffect, useRef, useState } from 'react';
import type { Operation } from '$lib/api/types';
import { formatDuration } from '$lib/format';
import { createOperationStream, operationDurationMs, operationStateLook, outputTone, type OperationStream } from '$lib/stacks/operation';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { useStore } from '../hooks/useStore';
import styles from './OperationPanel.module.css';

export interface OperationPanelProps {
  operationId: string;
  onCancel?: (id: string) => void | Promise<void>;
  onFinished?: (op: Operation) => void;
  autoscroll?: boolean;
}

/**
 * Follows one operation's output. The stream is opened in an effect (not a render-time initializer) so
 * StrictMode's mount, cleanup, mount cycle closes and reopens it instead of leaving a closed stream or an
 * orphaned one; callers key the panel on the id.
 */
export function OperationPanel(props: OperationPanelProps) {
  const [stream, setStream] = useState<OperationStream>();
  const { operationId } = props;
  useEffect(() => {
    const opened = createOperationStream(operationId);
    setStream(opened);
    return () => opened.close();
  }, [operationId]);
  return stream ? <OperationOutput key={operationId} stream={stream} {...props} /> : null;
}

function OperationOutput({ stream, operationId, onCancel, onFinished, autoscroll = true }: OperationPanelProps & { stream: OperationStream }) {
  const lines = useStore(stream.lines);
  const op = useStore(stream.op);
  const status = useStore(stream.status);
  const unavailable = useStore(stream.unavailable);

  const region = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const [now, setNow] = useState(() => Date.now());
  const [cancelling, setCancelling] = useState(false);
  const cancelInFlight = useRef(false);
  const notified = useRef(false);
  const finishedHandler = useRef(onFinished);
  finishedHandler.current = onFinished;

  const running = !unavailable && (!op || op.state === 'running');
  const look = op ? operationStateLook(op.state) : undefined;
  const elapsedMs = op ? operationDurationMs(op, now) : undefined;
  const announcement = unavailable
    ? 'Operation no longer available'
    : op && op.state !== 'running'
      ? `${op.action} ${op.state}`
      : status === 'reconnecting'
        ? 'Connection lost, reconnecting'
        : '';

  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [running]);

  useEffect(() => {
    if (op && op.state !== 'running' && !notified.current) {
      notified.current = true;
      finishedHandler.current?.(op);
    }
  }, [op]);

  useEffect(() => {
    if (autoscroll && following && region.current) region.current.scrollTop = region.current.scrollHeight;
  }, [lines, autoscroll, following]);

  async function cancel() {
    if (cancelInFlight.current || !onCancel) return;
    cancelInFlight.current = true;
    setCancelling(true);
    const result = onCancel(operationId);
    // A synchronous handler stays "cancelling" until the operation ends and the button goes away.
    if (result instanceof Promise) {
      try {
        await result;
      } catch {
        /* the caller reports its own errors */
      } finally {
        cancelInFlight.current = false;
        setCancelling(false);
      }
    }
  }

  function onScroll() {
    const el = region.current;
    if (el) setFollowing(el.scrollHeight - el.scrollTop - el.clientHeight < 24);
  }

  function jump() {
    setFollowing(true);
    if (region.current) region.current.scrollTop = region.current.scrollHeight;
  }

  return (
    <section className={styles.panel} aria-label="Operation output">
      <header className={styles.header}>
        {op && look ? (
          <>
            <strong className={styles.cmd}>{op.stack ? `docker compose ${op.action}` : op.action}</strong>
            <span className={styles.state} data-tone={look.tone}>
              {look.label}
            </span>
            <span className={styles.meta}>
              {elapsedMs === undefined ? '' : formatDuration(elapsedMs)} · exit {op.exitCode ?? '—'}
            </span>
          </>
        ) : unavailable ? (
          <span className={styles.muted}>Operation no longer available</span>
        ) : (
          <span className={styles.muted}>Connecting…</span>
        )}
        <span className={styles.spacer} />
        {running && (
          <Button variant="secondary" size="sm" loading={cancelling} onClick={() => void cancel()}>
            Cancel
          </Button>
        )}
        <CopyButton text={lines.map((entry) => entry.text).join('\n')} label="Copy output" size="xs" />
      </header>
      {op?.error && <p className={styles.error}>{op.error}</p>}
      <div className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </div>
      <div className={styles.wrap}>
        <div ref={region} className={styles.log} role="log" aria-live="off" aria-label="Output" tabIndex={0} onScroll={onScroll}>
          {lines.map((entry) => (
            <div key={entry.key} className={styles.line} data-kind={entry.kind !== 'line' ? entry.kind : undefined} data-tone={entry.kind === 'line' ? outputTone(entry.text) : undefined}>
              {entry.text}
            </div>
          ))}
        </div>
        {!following && (
          <button type="button" className={styles.jump} onClick={jump}>
            Jump to latest
          </button>
        )}
      </div>
    </section>
  );
}
