import { useLayoutEffect, useRef } from 'react';
import { Link } from 'react-router';
import type { ContainerRow } from '$lib/api/types';
import { consoleStatus, logClock, logUtc } from '$lib/stacks/console';
import { lineTone } from '$lib/stacks/logs';
import { useLogStream } from '../detail/useLogStream';
import { ContainerActions } from './ContainerActions';
import styles from './ContainerPeek.module.css';
import type { ContainerAction } from './useContainerAction';

const PEEK_LINES = 8;

export interface ContainerPeekProps {
  row: ContainerRow;
  /** Id of the peek section; the row's toggle points at it with aria-controls. */
  id: string;
  mobile: boolean;
  /** Why the actions are unavailable right now, or undefined when they are allowed. */
  reason: string | undefined;
  onRequest: (action: ContainerAction) => void;
}

/** The last few log lines of one container, streamed while mounted. A recreated container arrives under a new id, which remounts the enclosing keyed fragment and so opens a new stream. */
export function ContainerPeek({ row, id, mobile, reason, onRequest }: ContainerPeekProps) {
  const { lines, status, ended, errors } = useLogStream({ kind: 'container', id: row.id }, { tail: PEEK_LINES, follow: true });
  const state = consoleStatus({ status, ended, paused: false, sourceKind: 'container', containerState: row.state });
  const shown = lines.slice(-PEEK_LINES);
  const error = errors[0];

  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <div role="row" className={styles.peekRow}>
      <div role="cell" aria-colspan={10} className={styles.cell}>
        <section id={id} aria-label={`Logs of ${row.name}`} className={styles.peek}>
          <div className={styles.header}>
            <span role="status" className={styles.status} data-tone={state.tone}>
              {state.tone === 'live' ? (
                <>
                  <span className={styles.dot} aria-hidden="true" />
                  streaming
                </>
              ) : (
                state.label
              )}
            </span>
            {!mobile && <span className={styles.dockerStatus}>{row.status}</span>}
            <span className={styles.spacer} />
            <span className={styles.actions}>
              <ContainerActions row={row} size="md" reason={reason} onRequest={onRequest} />
            </span>
            <Link to={`/containers/${encodeURIComponent(row.name)}?tab=logs`} className={styles.full}>
              Open full logs
            </Link>
          </div>
          {error && (
            <div role="alert" className={styles.error}>
              {error.service} ({error.container}): {error.error}
            </div>
          )}
          <div ref={scroller} role="log" aria-live="off" aria-label="Log output" tabIndex={0} className={styles.lines}>
            {shown.map((line) => (
              <div key={line.seq} data-testid="peek-row" data-tone={lineTone(line.text)} className={styles.line}>
                <time className={styles.time} title={logUtc(line.time)}>
                  {logClock(line.time)}
                </time>
                <span className={styles.text}>{line.text}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
