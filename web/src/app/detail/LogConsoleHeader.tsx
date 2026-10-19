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
  status: { label: string; tone: 'live' | 'warning' | 'muted' };
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
  status,
  latest,
}: LogConsoleHeaderProps) {
  const pillLabel = !paused ? 'Following' : newCount > 0 ? `Paused · ${newCount} new` : 'Paused';
  return (
    <header className={styles.header}>
      <button type="button" className={styles.toggle} aria-expanded={open} onClick={onToggle}>
        <svg className={styles.chevron} width="14" height="14" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M6 15l6-6 6 6" fill="none" stroke="var(--muted)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
        Logs
      </button>
      <button type="button" className={styles.pill} aria-pressed={paused} data-paused={paused} onClick={onTogglePause}>
        {!paused && <span className={styles.dot} data-dot />}
        {pillLabel}
      </button>
      {open ? (
        <>
          <span className={styles.spacer} />
          <Segmented label="Level" size="sm" options={LOG_LEVELS} value={level} onChange={(value) => onLevel(value as LogLevel)} />
          <input
            type="search"
            className={styles.search}
            aria-label="Search logs"
            placeholder="Search logs"
            value={search}
            onChange={(e) => onSearch(e.target.value)}
          />
        </>
      ) : (
        <span className={styles.latest}>
          {latest && (
            <>
              <span>{logClock(latest.time)}</span> <span>{latest.service}</span> <span>{latest.text}</span>
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
