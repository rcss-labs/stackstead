import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import type { HostSnapshot, StackSummary } from '$lib/api/types';
import { panelLabel, statusFragments, statusSentence } from '$lib/dashboard/overview';
import type { HostSample } from '$lib/stores/host';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { RelativeTime } from '../components/RelativeTime';
import { HostStats } from './HostStats';
import { StackTowers } from './StackTowers';
import styles from './StatusPanel.module.css';

export interface StatusPanelProps {
  items: StackSummary[];
  host: HostSnapshot | undefined;
  history: HostSample[];
  lastEventIso?: string;
  /** Import / New stack; off while the empty state carries its own links. */
  showActions: boolean;
}

/** The health-first header card: one sentence, the stack towers and the host strip. */
export function StatusPanel({ items, host, history, lastEventIso, showActions }: StatusPanelProps) {
  const label = panelLabel(items);
  const fragments = items.length > 0 ? statusFragments(items, host) : [];

  return (
    <div className={styles.card}>
      <div className={styles.head}>
        <div className={styles.summary}>
          <div className={styles.label}>
            <Led tone={label.tone} pulse={label.pulse} />
            <PanelLabel tone={label.tone} as="span">
              {label.text}
            </PanelLabel>
          </div>
          <h1 className={styles.sentence}>{statusSentence(items)}</h1>
          {items.length > 0 && (
            <p className={styles.sub}>
              {fragments.join(' · ')}
              {lastEventIso && (
                <>
                  {' · last event '}
                  <RelativeTime iso={lastEventIso} />
                </>
              )}
            </p>
          )}
        </div>
        {showActions && (
          <div className={styles.actions}>
            <Link to="/stacks/import" className={styles.action}>
              Import
            </Link>
            <Link to="/stacks/new" className={styles.action} data-primary="true">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
                <path d={ICONS.plus} />
              </svg>
              New stack
            </Link>
          </div>
        )}
      </div>
      <StackTowers items={items} />
      <HostStats host={host} history={history} />
    </div>
  );
}
