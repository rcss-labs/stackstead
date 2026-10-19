import { useEffect, useLayoutEffect, useRef, type KeyboardEvent } from 'react';
import type { LogError } from '$lib/api/types';
import type { LogLine } from '$lib/logs/buffer';
import { logClock, logUtc, searchSegments } from '$lib/stacks/console';
import { lineTone } from '$lib/stacks/logs';
import { Tag } from '../components/Tag';
import { seriesVar } from './LogChips';
import styles from './LogLines.module.css';

/** Distance from the bottom within which the view keeps following new lines. */
const STICK_THRESHOLD = 24;

export interface LogLinesProps {
  lines: LogLine[];
  search: string;
  pinned: number | undefined;
  onPin: (seq: number) => void;
  paused: boolean;
  newCount: number;
  onJumpToLatest: () => void;
  ended: boolean;
  dropped: number;
  errors: LogError[];
  onDismissError: (error: LogError) => void;
  onReconnect: () => void;
  onClear: () => void;
  colorIndex: (service: string) => number;
  empty: boolean;
}

export function LogLines({
  lines,
  search,
  pinned,
  onPin,
  paused,
  newCount,
  onJumpToLatest,
  ended,
  dropped,
  errors,
  onDismissError,
  onReconnect,
  onClear,
  colorIndex,
  empty,
}: LogLinesProps) {
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  function onScroll() {
    const el = scroller.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < STICK_THRESHOLD;
  }

  // Resuming jumps to the newest line, as the pill promises.
  useEffect(() => {
    if (!paused) stick.current = true;
  }, [paused]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current && !paused) el.scrollTop = el.scrollHeight;
  }, [lines, paused]);

  function onRowKey(e: KeyboardEvent<HTMLDivElement>, seq: number) {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    onPin(seq);
  }

  return (
    <>
      <div className={styles.notices}>
        {dropped > 0 && <span className={styles.note}>{dropped} older lines dropped</span>}
        {paused && newCount > 0 && (
          <button type="button" className={styles.jump} onClick={onJumpToLatest}>
            Jump to latest
          </button>
        )}
        <button type="button" className={styles.clear} aria-label="Clear log" title="Clear log" onClick={onClear}>
          <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true">
            <path d="M4 7h16M10 11v6M14 11v6M6 7l1 12h10l1-12M9 7V4h6v3" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </button>
      </div>
      {errors.map((e) => (
        <div key={`${e.container}\0${e.error}`} className={styles.error} role="alert">
          <strong>
            {e.service} ({e.container})
          </strong>
          <span>{e.error}</span>
          <button type="button" aria-label={`Dismiss error for ${e.container}`} onClick={() => onDismissError(e)}>
            Dismiss
          </button>
        </div>
      ))}
      <div ref={scroller} className={styles.scroller} role="log" aria-live="off" aria-label="Log output" tabIndex={0} onScroll={onScroll}>
        {lines.map((line) => {
          const tone = lineTone(line.text);
          const isPinned = pinned === line.seq;
          return (
            <div
              key={line.seq}
              className={styles.row}
              role="button"
              tabIndex={0}
              data-testid="log-row"
              data-tone={tone}
              data-pinned={isPinned || undefined}
              aria-pressed={isPinned}
              onClick={() => onPin(line.seq)}
              onKeyDown={(e) => onRowKey(e, line.seq)}
            >
              <time className={styles.time} dateTime={line.time} title={logUtc(line.time)}>
                {logClock(line.time)}
              </time>
              <span className={styles.service} style={{ color: seriesVar(colorIndex(line.service)) }}>
                {line.service}
                {line.stream === 'stderr' && <Tag tone="danger">stderr</Tag>}
              </span>
              <span className={styles.text}>
                {searchSegments(line.text, search).map((segment, i) => (segment.match ? <mark key={i}>{segment.text}</mark> : segment.text))}
              </span>
            </div>
          );
        })}
        {empty && <div className={styles.empty}>No lines match.</div>}
        {ended && (
          <div className={styles.end}>
            <span>end of log</span>
            <button type="button" onClick={onReconnect}>
              Reconnect
            </button>
          </div>
        )}
      </div>
    </>
  );
}
