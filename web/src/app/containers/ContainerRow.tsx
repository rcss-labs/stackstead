import type { MouseEvent } from 'react';
import { Link } from 'react-router';
import type { ContainerRow as ContainerRowData } from '$lib/api/types';
import { containerLed, isProblem } from '$lib/containers/list';
import { formatBytes, formatPercent } from '$lib/format';
import { portChips } from '$lib/stacks/summary';
import { Button } from '../components/Button';
import { Led } from '../components/Led';
import { StatusBadge } from '../components/StatusBadge';
import { Tag } from '../components/Tag';
import styles from './ContainerRow.module.css';

export interface ContainerRowProps {
  row: ContainerRowData;
  index: number;
  focused: boolean;
  open: boolean;
  /** Id of the peek region this row's toggle controls. */
  peekId: string;
  mobile: boolean;
  onFocus: (index: number) => void;
  onToggle: () => void;
  rowRef: (el: HTMLDivElement | null) => void;
}

export function ContainerRow({ row, index, focused, open, peekId, mobile, onFocus, onToggle, rowRef }: ContainerRowProps) {
  const led = containerLed(row);
  const ports = portChips(row.ports, 2);
  const cpu = row.cpu !== null ? formatPercent(row.cpu) : '—';
  const mem = row.mem !== null ? formatBytes(row.mem) : '—';

  // Links and buttons inside the row do their own thing; only a click on the row body toggles the peek.
  function handleClick(event: MouseEvent<HTMLDivElement>) {
    if ((event.target as HTMLElement).closest('a, button')) return;
    onToggle();
  }

  return (
    <div
      ref={rowRef}
      role="row"
      className={styles.row}
      tabIndex={focused ? 0 : -1}
      data-focused={focused ? 'true' : undefined}
      data-open={open ? 'true' : undefined}
      aria-expanded={open}
      data-container={row.name}
      onFocus={() => onFocus(index)}
      onClick={handleClick}
    >
      <div role="cell" className={styles.led}>
        <Led tone={led.tone} pulse={led.pulse} label={row.state} />
      </div>
      <div role="cell" className={styles.name}>
        <Link to={`/containers/${encodeURIComponent(row.name)}`} className={styles.nameLink} title={row.name}>
          {row.name}
        </Link>
        {mobile && (
          <span className={styles.status} data-tone={isProblem(row) ? 'danger' : undefined}>
            {row.status}
          </span>
        )}
      </div>
      <div role="cell" data-col="stack" data-desktop className={styles.stack}>
        {row.managed === 'stack' ? (
          <Link to={`/stacks/${encodeURIComponent(row.stack)}`} className={styles.stackLink}>
            {row.stack}
          </Link>
        ) : row.managed === 'compose' ? (
          <span className={styles.project}>
            {row.project}
            <Tag>unregistered</Tag>
          </span>
        ) : (
          <span className={styles.dash}>—</span>
        )}
      </div>
      <div role="cell" data-col="image" data-desktop className={styles.image} title={row.image}>
        {row.image}
      </div>
      <div role="cell" data-col="state" data-desktop className={styles.state}>
        <StatusBadge status={row.state} />
        {row.health !== 'none' && (
          <span className={styles.health} data-health={row.health}>
            {row.health}
          </span>
        )}
      </div>
      <div role="cell" data-col="ports" data-desktop className={styles.ports}>
        {ports.shown.length === 0 ? (
          <span className={styles.dash}>—</span>
        ) : (
          <span className={styles.portList} title={ports.title}>
            {ports.shown.map((p) => (
              <span key={p} className={styles.port}>
                {p}
              </span>
            ))}
            {ports.more > 0 && <span className={styles.more}>+{ports.more}</span>}
          </span>
        )}
      </div>
      <div role="cell" data-col="cpu" data-desktop className={styles.num}>
        <span>{cpu}</span>
      </div>
      <div role="cell" data-col="mem" data-desktop className={styles.num}>
        <span>{mem}</span>
      </div>
      <div role="cell" data-col="restarts" data-desktop className={styles.num}>
        <span data-tone={row.restarts24h >= 3 ? 'warning' : undefined}>{row.restarts24h}</span>
      </div>
      {mobile && (
        <div role="cell" data-mobile aria-label={`CPU ${cpu}, memory ${mem}`} className={styles.mobileStats}>
          <span>{cpu}</span>
          <span>{mem}</span>
        </div>
      )}
      <div role="cell" className={styles.toggle}>
        <Button
          variant="ghost"
          size="xs"
          icon
          aria-label={`Peek logs for ${row.name}`}
          aria-expanded={open}
          aria-controls={peekId}
          onClick={onToggle}
        >
          <svg className={styles.chevron} data-open={open ? 'true' : undefined} width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </Button>
      </div>
    </div>
  );
}
