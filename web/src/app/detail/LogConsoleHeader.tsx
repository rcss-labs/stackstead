import { LOG_LEVELS, type LogLevel } from '$lib/stacks/console';
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
        <svg className={styles.chevron} width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
          <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
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
        <span className={styles.latest}>{latest?.text}</span>
      )}
      <span className={styles.status} role="status" data-tone={status.tone}>
        {status.label}
      </span>
    </header>
  );
}
