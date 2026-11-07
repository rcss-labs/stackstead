import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import type { HostSnapshot, StackSummary } from '$lib/api/types';
import type { HostSample } from '$lib/stores/host';
import { RelativeTime } from '../components/RelativeTime';
import { HostStats } from './HostStats';
import { StackTowers } from './StackTowers';
import styles from './StatusPanel.module.css';

import { useServer } from '../servers/ServerContext';
export interface StatusPanelProps {
  items: StackSummary[];
  /** The page's verdict from `headline()`; the panel never words health itself. */
  headline: { title: string; sub: string };
  host: HostSnapshot | undefined;
  history: HostSample[];
  lastEventIso?: string;
  /** Import / New stack; off while the empty state carries its own links. */
  showActions: boolean;
  /** Days until the disk fills at the current rate; absent or null draws no line. */
  forecastDays?: number | null;
  /** Tower links and keys for a list that spans servers. */
  hrefOf?: (stack: StackSummary) => string;
  keyOf?: (stack: StackSummary) => string;
}

function forecastLine(days: number): string {
  if (days < 1) return 'Less than a day until the disk is full at the current rate.';
  const rounded = Math.round(days);
  return `About ${rounded} ${rounded === 1 ? 'day' : 'days'} until the disk is full at the current rate.`;
}

/** The page head: one plain sentence about the host, then the stack towers and the host strip. */
export function StatusPanel({ items, headline, host, history, lastEventIso, showActions, forecastDays, hrefOf, keyOf }: StatusPanelProps) {
  const { href } = useServer();

  return (
    <div className={styles.card}>
      <div className={styles.head}>
        <div className={styles.summary}>
          <h1 className={styles.sentence}>{headline.title}</h1>
          {(headline.sub || lastEventIso) && (
            <p className={styles.sub}>
              {headline.sub}
              {lastEventIso && (
                <>
                  {headline.sub && ' · '}
                  {'last event '}
                  <RelativeTime iso={lastEventIso} />
                </>
              )}
            </p>
          )}
        </div>
        {showActions && (
          <div className={styles.actions}>
            <Link to={href('/stacks/import')} className={styles.action}>
              Import
            </Link>
            <Link to={href('/stacks/new')} className={styles.action} data-primary="true">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
                <path d={ICONS.plus} />
              </svg>
              New stack
            </Link>
          </div>
        )}
      </div>
      <StackTowers items={items} hrefOf={hrefOf} keyOf={keyOf} />
      <HostStats host={host} history={history} />
      {typeof forecastDays === 'number' && <p className={styles.forecast}>{forecastLine(forecastDays)}</p>}
    </div>
  );
}
