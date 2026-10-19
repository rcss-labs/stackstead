import type { MouseEvent } from 'react';
import { Link, useNavigate } from 'react-router';
import type { Action, ProjectSummary, StackSummary } from '$lib/api/types';
import { rowPrimaryAction } from '$lib/dashboard/overview';
import { formatBytes, formatPercent } from '$lib/format';
import { ICONS } from '$lib/shell/icons';
import { healthCell, stackNeedsAttention } from '$lib/stacks/summary';
import { Button } from '../components/Button';
import { Led, type LedProps } from '../components/Led';
import { ServiceCells } from '../components/ServiceCells';
import { Sparkline } from '../components/Sparkline';
import { Spinner } from '../components/Spinner';
import { Tag } from '../components/Tag';
import { PortLinks } from './PortLinks';
import styles from './StackRow.module.css';

export interface StackRowProps {
  stack: StackSummary;
  /** Services with a container problem, from the alerts. */
  failing: string[];
  usage: ProjectSummary | undefined;
  /** An operation is running on this stack. */
  running: boolean;
  /** Why lifecycle actions are unavailable, or undefined. */
  reason: string | undefined;
  /** An action request for this stack is in flight. */
  acting: boolean;
  onAct: (name: string, action: Action) => void;
}

function ledFor(s: StackSummary): { tone: LedProps['tone']; pulse: boolean } {
  if (s.status === 'unavailable') return { tone: 'danger', pulse: false };
  if (stackNeedsAttention(s)) return { tone: 'warning', pulse: true };
  if (s.status === 'running') return { tone: 'success', pulse: false };
  return { tone: s.status === 'stopped' ? 'faint' : 'muted', pulse: false };
}

export function StackRow({ stack: s, failing, usage, running, reason, acting, onAct }: StackRowProps) {
  const navigate = useNavigate();
  const href = `/stacks/${encodeURIComponent(s.name)}`;
  const health = healthCell(s.health);
  const primary = rowPrimaryAction(s);
  const led = ledFor(s);
  const cpu = usage?.cpuNow != null ? formatPercent(usage.cpuNow) : '—';
  const mem = usage?.memNow != null ? formatBytes(usage.memNow) : '—';

  function openFromRow(e: MouseEvent<HTMLDivElement>) {
    // Links and buttons inside the row do their own thing.
    if ((e.target as Element).closest('a, button')) return;
    navigate(href);
  }

  const cells = <ServiceCells count={s.serviceCount} good={health.good} bad={health.bad} starting={health.starting} title={health.title} />;

  return (
    <div role="row" className={styles.row} data-stack={s.name} onClick={openFromRow}>
      <div role="cell" className={styles.led}>
        <Led tone={led.tone} pulse={led.pulse} />
        <span className="visually-hidden">{s.status}</span>
      </div>
      <div role="cell" className={styles.name}>
        <div className={styles.nameLine}>
          <Link to={href} className={styles.nameLink} title={`project ${s.project}`}>
            {s.name}
          </Link>
          {s.origin === 'external' && <Tag>imported</Tag>}
          {running && <Spinner label={`Operation running on ${s.name}`} />}
        </div>
        {failing.length > 0 ? (
          <div className={styles.sub} data-tone="warning">
            {failing.join(', ')} down
          </div>
        ) : (
          <div className={styles.sub} title={s.path}>
            {s.path}
          </div>
        )}
      </div>
      <div role="cell" className={styles.services} data-desktop="true">
        {cells}
        <span className={styles.mono}>
          {s.running}/{s.serviceCount}
        </span>
      </div>
      <div role="cell" data-desktop="true">
        <PortLinks ports={s.ports} />
      </div>
      <div role="cell" className={styles.metric} data-desktop="true">
        <Sparkline values={usage?.cpu ?? []} tone="accent" width={52} height={16} />
        <span className={styles.value}>{cpu}</span>
      </div>
      <div role="cell" className={styles.metric} data-desktop="true">
        <Sparkline values={usage?.mem ?? []} tone="info" width={52} height={16} />
        <span className={styles.value}>{mem}</span>
      </div>
      <div role="cell" className={styles.actions} data-desktop="true">
        <Button
          variant="ghost"
          size="sm"
          icon
          aria-label={`${primary.label} ${s.name}`}
          title={`${primary.label} ${s.name}`}
          disabledReason={reason}
          loading={acting}
          onClick={() => onAct(s.name, primary.action)}
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill={primary.action === 'up' ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
            <path d={primary.action === 'up' ? ICONS.play : ICONS.restart} />
          </svg>
        </Button>
        <Link to={`${href}?tab=logs`} className={styles.logs}>
          Logs
        </Link>
      </div>
      <div role="cell" className={styles.mobile} aria-hidden="true">
        {cells}
        <span className={styles.mono}>
          {cpu} · {mem}
        </span>
      </div>
    </div>
  );
}
