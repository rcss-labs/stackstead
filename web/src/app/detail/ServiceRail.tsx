import type { ReactNode } from 'react';
import type { ServiceDetail } from '$lib/api/types';
import { formatBytes, formatPercent, formatRate } from '$lib/format';
import type { SeriesPoint } from '$lib/metrics/series';
import type { DetailItem } from '$lib/stacks/detailLocation';
import { totalsNow } from '$lib/stacks/liveMetrics';
import { serviceState } from '$lib/stacks/summary';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import styles from './ServiceRail.module.css';

export interface ServiceRailProps {
  services: ServiceDetail[];
  selected: DetailItem;
  operationsCount: number;
  latest: Map<string, SeriesPoint>;
  onSelect: (item: DetailItem) => void;
  /** Where the add-service control goes (a later plan fills it). */
  addSlot?: ReactNode;
}

const isService = (selected: DetailItem, name: string) => selected.kind === 'service' && selected.name === name;

function StackItemIcon({ d }: { d: string }) {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}

const FILE_ICON = 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6';
const OPS_ICON = 'M4 6h16M4 12h16M4 18h10';

function netText(totals: ReturnType<typeof totalsNow>): string | undefined {
  if (totals.netRx === undefined && totals.netTx === undefined) return undefined;
  const part = (v: number | undefined) => (v === undefined ? '—' : formatRate(v));
  return `${part(totals.netRx)} · ${part(totals.netTx)}`;
}

export function ServiceRail({ services, selected, operationsCount, latest, onSelect, addSlot }: ServiceRailProps) {
  const totals = totalsNow(latest);
  const net = netText(totals);
  const total = (text: string | undefined) => (text === undefined ? <dd data-empty="true">—</dd> : <dd>{text}</dd>);
  return (
    <nav className={styles.rail} aria-label="Services">
      <div className={styles.group}>
        <div className={styles.labelRow}>
          <PanelLabel>Services</PanelLabel>
          {addSlot}
        </div>
        {services.map((service) => {
          const state = serviceState(service);
          const cpu = latest.get(service.name)?.cpu;
          const current = isService(selected, service.name);
          return (
            <button
              key={service.name}
              type="button"
              className={styles.service}
              data-selected={current}
              data-item={service.name}
              aria-current={current ? 'true' : undefined}
              onClick={() => onSelect({ kind: 'service', name: service.name })}
            >
              <Led tone={state.tone} pulse={state.tone === 'danger'} />
              <span className={styles.nameBlock}>
                <span className={styles.name}>{service.name}</span>{' '}
                <span className={styles.state} data-tone={state.tone}>
                  {state.text}
                </span>
              </span>{' '}
              <span className={styles.cpu}>
                <span className={styles.cpuValue} data-empty={cpu === undefined}>
                  {cpu === undefined ? '—' : formatPercent(cpu)}
                </span>
                <span className={styles.bar} aria-hidden="true">
                  <span className={styles.fill} data-cpu-fill style={{ width: `${Math.min(100, cpu ?? 0)}%` }} />
                </span>
              </span>
            </button>
          );
        })}
      </div>
      <div className={styles.group}>
        <div className={styles.labelRow}>
          <PanelLabel>Stack</PanelLabel>
        </div>
        <button
          type="button"
          className={styles.item}
          data-selected={selected.kind === 'compose'}
          data-item="compose"
          aria-current={selected.kind === 'compose' ? 'true' : undefined}
          onClick={() => onSelect({ kind: 'compose' })}
        >
          <StackItemIcon d={FILE_ICON} />
          <span className={styles.itemLabel}>Compose file</span>
        </button>
        <button
          type="button"
          className={styles.item}
          data-selected={selected.kind === 'operations'}
          data-item="operations"
          aria-current={selected.kind === 'operations' ? 'true' : undefined}
          onClick={() => onSelect({ kind: 'operations' })}
        >
          <StackItemIcon d={OPS_ICON} />
          <span className={styles.itemLabel}>Operations</span>{' '}
          <span className={styles.aux}>{operationsCount}</span>
        </button>
      </div>
      <dl className={styles.totals}>
        <dt>CPU</dt>
        {total(totals.cpu === undefined ? undefined : formatPercent(totals.cpu))}
        <dt>Memory</dt>
        {total(totals.mem === undefined ? undefined : formatBytes(totals.mem))}
        <dt>Net</dt>
        {total(net)}
      </dl>
    </nav>
  );
}

/** The rail as a horizontal chip scroller for narrow screens; the same selection, shown under 760px only. */
export function ServiceChips({ services, selected, onSelect }: ServiceRailProps) {
  return (
    <nav className={styles.chips} aria-label="Service chips">
      {services.map((service) => {
        const state = serviceState(service);
        const current = isService(selected, service.name);
        return (
          <button
            key={service.name}
            type="button"
            className={styles.chip}
            data-selected={current}
            aria-pressed={current}
            onClick={() => onSelect({ kind: 'service', name: service.name })}
          >
            <span className={styles.square} data-tone={state.tone} data-pulse={state.tone === 'danger' ? 'true' : undefined} aria-hidden="true" />
            {service.name}
          </button>
        );
      })}
      <button type="button" className={styles.chip} data-plain data-selected={selected.kind === 'compose'} aria-pressed={selected.kind === 'compose'} onClick={() => onSelect({ kind: 'compose' })}>
        Compose
      </button>
      <button type="button" className={styles.chip} data-plain data-selected={selected.kind === 'operations'} aria-pressed={selected.kind === 'operations'} onClick={() => onSelect({ kind: 'operations' })}>
        Operations
      </button>
    </nav>
  );
}
