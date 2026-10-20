import type { Action, Alert, MetricsSummary, ProjectSummary, StackSummary } from '$lib/api/types';
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
  /** Per-row usage for lists that span servers (a project name is only unique within one); wins over `summary`. */
  usageOf?: (stack: StackSummary) => ProjectSummary | undefined;
  query: string;
  onQuery: (query: string) => void;
  /** Stacks with a running operation, from the shared store. */
  runningStacks: ReadonlySet<string>;
  reasonFor: (stack: StackSummary) => string | undefined;
  acting: ReadonlySet<string>;
  onAct: (name: string, action: Action, stack: StackSummary) => void;
  /** Adds the Server chip column; the slug is null for this server. */
  serverOf?: (stack: StackSummary) => { name: string; slug: string | null };
  /** Per-row link, for stacks that live on other servers. */
  hrefOf?: (stack: StackSummary) => string;
  /** Identity of a row in `runningStacks`, `acting` and React keys; defaults to the stack name. */
  keyOf?: (stack: StackSummary) => string;
}

const COLUMNS = ['Status', 'Stack', 'Services', 'Ports', 'CPU', 'Memory', 'Actions'];
const FLEET_COLUMNS = ['Status', 'Stack', 'Server', 'Services', 'Ports', 'CPU', 'Memory', 'Actions'];

/** The stacks card: filter, then one group per health bucket with a row per stack. */
export function StacksList({ items, alerts, summary, usageOf, query, onQuery, runningStacks, reasonFor, acting, onAct, serverOf, hrefOf, keyOf }: StacksListProps) {
  const columns = serverOf ? FLEET_COLUMNS : COLUMNS;
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
              {columns.map((name) => (
                <div key={name} role="columnheader">
                  {name}
                </div>
              ))}
            </div>
          </div>
          {groups.map((group) => (
            <div key={group.id} role="rowgroup">
              <div role="row" className={styles.groupRow} data-group={group.id}>
                <div role="cell" aria-colspan={columns.length}>
                  <PanelLabel as="div" tone={group.id === 'attention' ? 'warning' : 'muted'}>
                    {group.title} <span className={styles.groupCount}>{group.items.length}</span>
                  </PanelLabel>
                </div>
              </div>
              {group.items.map((s) => {
                const key = keyOf ? keyOf(s) : s.name;
                return (
                  <StackRow
                    key={key}
                    stack={s}
                    failing={failingServices(alerts, s.name)}
                    usage={usageOf ? usageOf(s) : summary?.stacks[s.project]}
                    running={runningStacks.has(key)}
                    reason={reasonFor(s)}
                    acting={acting.has(key)}
                    onAct={(name, action) => onAct(name, action, s)}
                    serverName={serverOf?.(s).name}
                    href={hrefOf?.(s)}
                  />
                );
              })}
            </div>
          ))}
        </div>
      </div>
      {groups.length === 0 && query.trim() !== '' && <p className={styles.empty}>No stacks match the current filter.</p>}
    </section>
  );
}
