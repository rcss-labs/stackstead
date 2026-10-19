import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react';
import {
  consoleStatus,
  LOGS_HEIGHT_MAX,
  LOGS_HEIGHT_MIN,
  LOGS_HEIGHT_MOBILE,
  newLinesSince,
  type LogLevel,
} from '$lib/stacks/console';
import type { LogSource } from '$lib/stacks/logs';
import styles from './LogConsole.module.css';
import { LogConsoleHeader } from './LogConsoleHeader';
import { useLogStream } from './useLogStream';
import { useLogsHeight } from './useLogsHeight';

const KEY_STEP = 20;

export interface LogConsoleProps {
  source: LogSource;
  services: string[];
  service: string | undefined;
  onService: (service: string | undefined) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mobile: boolean;
  /** Where the height persists; tests inject a stub. */
  storage?: Storage;
}

export function LogConsole({ source, services, service, open, onOpenChange, mobile, storage }: LogConsoleProps) {
  // A service the stack does not have (a stale ?service=) shows every service rather than an empty console.
  const serverService = source.kind === 'stack' && service !== undefined && services.includes(service) ? service : undefined;
  const stream = useLogStream(source, { service: serverService, tail: 200, follow: true });
  const { height, setHeight, commit } = useLogsHeight(storage);
  const [paused, setPaused] = useState(false);
  const [frozenSeq, setFrozenSeq] = useState(0);
  const [level, setLevel] = useState<LogLevel>('all');
  const [search, setSearch] = useState('');
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{ startY: number; startHeight: number } | undefined>(undefined);

  // A different server filter is a new stream: restart it (never the first run, the hook opened it with this one).
  const appliedService = useRef(serverService);
  const { restart } = stream;
  useEffect(() => {
    if (appliedService.current === serverService) return;
    appliedService.current = serverService;
    setPaused(false);
    restart({ service: serverService, tail: 200, follow: true });
  }, [serverService, restart]);

  const latestSeq = stream.lines.at(-1)?.seq ?? 0;
  const newCount = paused ? newLinesSince(stream.lines, frozenSeq) : 0;
  const status = consoleStatus({ status: stream.status, ended: stream.ended, paused, sourceKind: source.kind });

  function togglePause() {
    if (!paused) setFrozenSeq(latestSeq);
    setPaused(!paused);
  }

  function onPointerDown(e: PointerEvent<HTMLDivElement>) {
    drag.current = { startY: e.clientY, startHeight: height };
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      // Not every environment can capture; the move events still arrive from the handle.
    }
    setDragging(true);
  }
  function onPointerMove(e: PointerEvent<HTMLDivElement>) {
    if (!drag.current) return;
    setHeight(drag.current.startHeight - (e.clientY - drag.current.startY));
  }
  function endDrag() {
    if (!drag.current) return;
    drag.current = undefined;
    setDragging(false);
    commit();
  }
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    e.preventDefault();
    setHeight(height + (e.key === 'ArrowUp' ? KEY_STEP : -KEY_STEP));
    commit();
  }

  const bodyHeight = open ? (mobile ? LOGS_HEIGHT_MOBILE : height) : 0;
  return (
    <section className={styles.console} aria-label="Logs" data-open={open}>
      {open && !mobile && (
        <div
          className={styles.handle}
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize logs"
          aria-valuemin={LOGS_HEIGHT_MIN}
          aria-valuemax={LOGS_HEIGHT_MAX}
          aria-valuenow={height}
          tabIndex={0}
          title="Drag to resize"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={endDrag}
          onPointerCancel={endDrag}
          onKeyDown={onKeyDown}
        >
          <span className={styles.grip} />
        </div>
      )}
      <LogConsoleHeader
        open={open}
        onToggle={() => onOpenChange(!open)}
        paused={paused}
        newCount={newCount}
        onTogglePause={togglePause}
        level={level}
        onLevel={setLevel}
        search={search}
        onSearch={setSearch}
        status={status}
        latest={stream.lines.at(-1)}
      />
      <div className={styles.body} data-testid="log-console-body" data-dragging={dragging || undefined} style={{ height: bodyHeight }} />
    </section>
  );
}
