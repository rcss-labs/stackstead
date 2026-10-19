import { LOG_LEVELS, logClock, type LogLevel } from '$lib/stacks/console';
import type { LogLine } from '$lib/logs/buffer';
import { Segmented } from '../components/Segmented';
import styles from './LogConsole.module.css';

export interface LogConsoleHeaderProps {
  open: boolean;
  onToggle: () => void;
  paused: boolean;
  newCount: number;
  onTogglePause: () => void;
  level: LogLevel;
  onLevel: (level: LogLevel) => void;
  search: string;
  onSearch: (search: string) => void;
  onClear: () => void;
  status: { label: string; tone: 'live' | 'warning' | 'muted' };
  /** The stream is over (ended, gone, closed or unauthorised): nothing is being followed. */
  stopped: boolean;
  latest: LogLine | undefined;
}

export function LogConsoleHeader({
  open,
  onToggle,
  paused,
  newCount,
  onTogglePause,
  level,
  onLevel,
  search,
  onSearch,
  onClear,
  status,
  stopped,
  latest,
}: LogConsoleHeaderProps) {
  const pillLabel = stopped ? 'Ended' : !paused ? 'Following' : newCount > 0 ? `Paused · ${newCount} new` : 'Paused';
  return (
    <header className={styles.header} data-testid="log-console-header">
      <button type="button" className={styles.toggle} aria-expanded={open} onClick={onToggle}>
        <svg className={styles.chevron} width="12" height="12" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M6 15l6-6 6 6" fill="none" stroke="var(--muted)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
        Logs
      </button>
      <button
        type="button"
        className={styles.pill}
        aria-pressed={stopped ? undefined : paused}
        data-paused={!stopped && paused}
        data-stopped={stopped || undefined}
        disabled={stopped}
        onClick={onTogglePause}
      >
        <span className={styles.dot} data-dot data-pulse={(!stopped && !paused) || undefined} />
        {pillLabel}
      </button>
      {open ? (
        <>
          <span className={styles.spacer} />
          <Segmented label="Level" size="sm" className={styles.level} options={LOG_LEVELS} value={level} onChange={(value) => onLevel(value as LogLevel)} />
          <input
            type="search"
            className={styles.search}
            aria-label="Search logs"
            placeholder="Search logs"
            value={search}
            onChange={(e) => onSearch(e.target.value)}
          />
          <button type="button" className={styles.clear} aria-label="Clear log" title="Clear log" onClick={onClear}>
            <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true">
              <path d="M4 7h16M10 11v6M14 11v6M6 7l1 12h10l1-12M9 7V4h6v3" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
        </>
      ) : (
        <span className={styles.latest}>
          {latest && (
            <>
              <span>{logClock(latest.time)}</span>{'  '}<span>{latest.service}</span>{'  '}<span>{latest.text}</span>
            </>
          )}
        </span>
      )}
      <span className={styles.status} role="status" data-tone={status.tone}>
        {status.label}
      </span>
    </header>
  );
}
