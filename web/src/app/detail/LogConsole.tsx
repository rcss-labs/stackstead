import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react';
import type { LogError } from '$lib/api/types';
import type { LogLine } from '$lib/logs/buffer';
import {
  consoleStatus,
  filterConsoleLines,
  LOGS_HEIGHT_MAX,
  LOGS_HEIGHT_MIN,
  LOGS_HEIGHT_MOBILE,
  newLinesSince,
  type LogLevel,
} from '$lib/stacks/console';
import type { LogSource } from '$lib/stacks/logs';
import styles from './LogConsole.module.css';
import { LogChips } from './LogChips';
import { LogConsoleHeader } from './LogConsoleHeader';
import { LogLines } from './LogLines';
import { useLogStream } from './useLogStream';
import { useLogsHeight } from './useLogsHeight';

const KEY_STEP = 20;
const STREAM_OPTIONS = { tail: 200, follow: true } as const;

const errorKey = (e: LogError) => `${e.container}\0${e.error}`;

/** Stable colour slot: a service's position among the sorted names; unlisted names fall back to a hash. */
function serviceColorIndex(sorted: string[], service: string): number {
  const at = sorted.indexOf(service);
  if (at >= 0) return at;
  let h = 0;
  for (let i = 0; i < service.length; i++) h = (h * 31 + service.charCodeAt(i)) >>> 0;
  return h;
}

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

export function LogConsole({ source, services, service, onService, open, onOpenChange, mobile, storage }: LogConsoleProps) {
  // A service the stack does not have (a stale ?service=) shows every service rather than an empty console.
  const serverService = source.kind === 'stack' && service !== undefined && services.includes(service) ? service : undefined;
  const stream = useLogStream(source, { service: serverService, tail: 200, follow: true });
  const { height, setHeight, commit } = useLogsHeight(storage);
  const [paused, setPaused] = useState(false);
  const [frozen, setFrozen] = useState<LogLine[]>([]);
  const [frozenSeq, setFrozenSeq] = useState(0);
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
  const [pinned, setPinned] = useState<number | undefined>();
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
    resetView();
    restart({ service: serverService, ...STREAM_OPTIONS });
  }, [serverService, restart]);

  // A new stream starts a new view: nothing frozen, dismissed or pinned carries over.
  function resetView() {
    setPaused(false);
    setFrozen([]);
    setFrozenSeq(0);
    setDismissed(new Set());
    setPinned(undefined);
  }

  const latestSeq = stream.lines.at(-1)?.seq ?? 0;
  const view = paused ? frozen : stream.lines;
  const visible = useMemo(() => filterConsoleLines(view, { level, search }), [view, level, search]);
  const newCount = paused ? newLinesSince(stream.lines, frozenSeq) : 0;
  const status = consoleStatus({ status: stream.status, ended: stream.ended, paused, sourceKind: source.kind });
  const sortedServices = useMemo(() => [...services].sort(), [services]);
  const visibleErrors = stream.errors.filter((e) => !dismissed.has(errorKey(e)));

  function togglePause() {
    if (!paused) {
      setFrozen(stream.lines);
      setFrozenSeq(latestSeq);
    }
    setPaused(!paused);
  }

  function reconnect() {
    resetView();
    stream.restart({ service: serverService, ...STREAM_OPTIONS });
  }

  function clear() {
    stream.clear();
    setFrozen([]);
    setFrozenSeq(latestSeq);
    setPinned(undefined);
  }

  function onPointerDown(e: PointerEvent<HTMLDivElement>) {
    e.preventDefault();
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
      <div className={styles.body} data-testid="log-console-body" data-dragging={dragging || undefined} style={{ height: bodyHeight }} inert={!open}>
        {source.kind === 'stack' && (
          <LogChips
            services={sortedServices}
            selected={serverService}
            onSelect={onService}
            colorIndex={(name) => serviceColorIndex(sortedServices, name)}
          />
        )}
        <LogLines
          lines={visible}
          search={search}
          pinned={pinned}
          onPin={(seq) => setPinned((current) => (current === seq ? undefined : seq))}
          paused={paused}
          newCount={newCount}
          onJumpToLatest={togglePause}
          ended={stream.ended}
          dropped={stream.dropped}
          errors={visibleErrors}
          onDismissError={(e) => setDismissed((current) => new Set(current).add(errorKey(e)))}
          onReconnect={reconnect}
          onClear={clear}
          colorIndex={(name) => serviceColorIndex(sortedServices, name)}
          empty={view.length > 0 && visible.length === 0}
        />
      </div>
    </section>
  );
}
