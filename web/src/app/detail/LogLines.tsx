import { useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import type { LogError } from '$lib/api/types';
import type { LogLine } from '$lib/logs/buffer';
import { logClock, logUtc, searchSegments } from '$lib/stacks/console';
import { lineTone } from '$lib/stacks/logs';
import { Tag } from '../components/Tag';
import { errorKey, seriesVar } from './logConsoleShared';
import styles from './LogLines.module.css';

/** Distance from the bottom within which the view keeps following new lines. */
const STICK_THRESHOLD = 24;
/** How long a new line keeps its flash marker; matches the lineIn animation. */
const FLASH_MS = 1200;

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

  const wasPaused = useRef(paused);
  useLayoutEffect(() => {
    const el = scroller.current;
    // Resuming jumps to the newest line, as the pill promises, wherever the view was scrolled.
    if (wasPaused.current && !paused) stick.current = true;
    wasPaused.current = paused;
    if (el && stick.current && !paused) el.scrollTop = el.scrollHeight;
  }, [lines, paused]);

  // Only lines newer than anything shown so far flash; rows revealed by a filter or a remount do not.
  const maxSeen = useRef(0);
  const flashed = useRef(new Map<number, number>());
  const now = Date.now();
  for (const [seq, at] of flashed.current) if (now - at >= FLASH_MS) flashed.current.delete(seq);
  const previousMax = maxSeen.current;
  for (const line of lines) if (line.seq > previousMax && !flashed.current.has(line.seq)) flashed.current.set(line.seq, now);
  maxSeen.current = Math.max(previousMax, lines.at(-1)?.seq ?? 0);

  // Roving tabindex: one row is a tab stop (the focused one, else the pinned one, else the last).
  const [focusedSeq, setFocusedSeq] = useState<number | undefined>();
  const tabStop = [focusedSeq, pinned].find((seq) => seq !== undefined && lines.some((l) => l.seq === seq)) ?? lines.at(-1)?.seq;

  function onRowKey(e: KeyboardEvent<HTMLDivElement>, seq: number) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onPin(seq);
      return;
    }
    const row = e.currentTarget;
    let target: Element | null | undefined;
    if (e.key === 'ArrowDown') target = row.nextElementSibling;
    else if (e.key === 'ArrowUp') target = row.previousElementSibling;
    else if (e.key === 'Home') target = row.parentElement?.firstElementChild;
    else if (e.key === 'End') target = row.parentElement?.querySelector('[data-testid="log-row"]:last-of-type') ?? row;
    else return;
    e.preventDefault();
    if (target instanceof HTMLElement && target.dataset.testid === 'log-row') target.focus();
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
        <div key={errorKey(e)} className={styles.error} role="alert">
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
              tabIndex={line.seq === tabStop ? 0 : -1}
              data-testid="log-row"
              data-tone={tone}
              data-new={flashed.current.has(line.seq) || undefined}
              data-pinned={isPinned || undefined}
              aria-pressed={isPinned}
              onFocus={() => setFocusedSeq(line.seq)}
              onClick={() => onPin(line.seq)}
              onKeyDown={(e) => onRowKey(e, line.seq)}
            >
              <time className={styles.time} dateTime={line.time} title={logUtc(line.time)}>
                {logClock(line.time)}
              </time>
              <span className={styles.service}>
                <span className={styles.serviceName} style={{ color: seriesVar(colorIndex(line.service)) }}>
                  {line.service}
                </span>
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
