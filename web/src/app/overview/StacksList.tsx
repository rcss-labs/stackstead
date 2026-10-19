import type { Action, Alert, MetricsSummary, StackSummary } from '$lib/api/types';
import { failingServices } from '$lib/dashboard/issues';
import { filterByQuery, groupStacks } from '$lib/dashboard/overview';
import { ICONS } from '$lib/shell/icons';
import { PanelLabel } from '../components/PanelLabel';
import { StackRow } from './StackRow';
import styles from './StacksList.module.css';

export interface StacksListProps {
  items: StackSummary[];
  alerts: Alert[] | undefined;
  summary: MetricsSummary | undefined;
  query: string;
  onQuery: (query: string) => void;
  /** Stacks with a running operation, from the shared store. */
  runningStacks: ReadonlySet<string>;
  reasonFor: (stack: StackSummary) => string | undefined;
  acting: ReadonlySet<string>;
  onAct: (name: string, action: Action) => void;
}

const COLUMNS = ['Stack', 'Services', 'Ports', 'CPU', 'Memory', 'Actions'];

/** The stacks card: filter, then one group per health bucket with a row per stack. */
export function StacksList({ items, alerts, summary, query, onQuery, runningStacks, reasonFor, acting, onAct }: StacksListProps) {
  const visible = filterByQuery(items, query);
  const groups = groupStacks(visible);

  return (
    <section className={styles.card}>
      <header className={styles.head}>
        <h2 id="stacks-heading" className={styles.title}>
          Stacks
        </h2>
        <span className={styles.count}>{visible.length === items.length ? items.length : `${visible.length} of ${items.length}`}</span>
        <label className={styles.search}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
            <path d={ICONS.search} />
          </svg>
          <input type="search" aria-label="Search stacks" placeholder="Filter" value={query} onChange={(e) => onQuery(e.target.value)} />
        </label>
      </header>
      <div className={styles.scroller}>
        <div role="table" aria-labelledby="stacks-heading" className={styles.table}>
          <div role="rowgroup" className="visually-hidden">
            <div role="row">
              {COLUMNS.map((name) => (
                <div key={name} role="columnheader">
                  {name}
                </div>
              ))}
            </div>
          </div>
          {groups.map((group) => (
            <div key={group.id} role="rowgroup">
              <div role="row" className={styles.groupRow} data-group={group.id}>
                <div role="cell" aria-colspan={6}>
                  <PanelLabel as="div" tone={group.id === 'attention' ? 'warning' : 'muted'}>
                    {group.title} <span className={styles.groupCount}>{group.items.length}</span>
                  </PanelLabel>
                </div>
              </div>
              {group.items.map((s) => (
                <StackRow
                  key={s.name}
                  stack={s}
                  failing={failingServices(alerts, s.name)}
                  usage={summary?.stacks[s.project]}
                  running={runningStacks.has(s.name)}
                  reason={reasonFor(s)}
                  acting={acting.has(s.name)}
                  onAct={onAct}
                />
              ))}
            </div>
          ))}
        </div>
      </div>
      {groups.length === 0 && query.trim() !== '' && <p className={styles.empty}>No stacks match the current filter.</p>}
    </section>
  );
}
