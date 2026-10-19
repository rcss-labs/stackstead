import { useState } from 'react';
import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { filterByQuery } from '$lib/dashboard/overview';
import { host, hostHistory } from '$lib/stores/host';
import { events } from '$lib/stores/live';
import { refreshStacks, stacks } from '$lib/stores/stacks';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useDashboard } from '../hooks/useDashboard';
import { useStore } from '../hooks/useStore';
import { StatusPanel } from '../overview/StatusPanel';
import styles from './Overview.module.css';

/** Health first: the status panel, then the stack list. Eager: the only screen in the entry chunk. */
export default function Overview() {
  const { items, loading, error } = useStore(stacks);
  const hostSnapshot = useStore(host);
  const history = useStore(hostHistory);
  const recentEvents = useStore(events);
  // Alerts, summary and storage feed the issue cards and the side column; the controller runs while the screen is mounted.
  useDashboard();
  const [query, setQuery] = useState('');
  const [retrying, setRetrying] = useState(false);

  async function retry() {
    setRetrying(true);
    try {
      await refreshStacks();
    } finally {
      setRetrying(false);
    }
  }

  const failedFirstLoad = Boolean(error) && items.length === 0;
  const degraded = items.length > 0 && items.every((s) => s.status === 'unknown');
  const visible = filterByQuery(items, query);

  if (loading && items.length === 0) {
    return (
      <section className={styles.page}>
        <div className={styles.loading}>
          <Spinner />
        </div>
      </section>
    );
  }

  return (
    <section className={styles.page}>
      {error && (
        <div>
          <Banner kind="danger" title="Could not load stacks">
            {error}
          </Banner>
          {failedFirstLoad && (
            <p>
              <Button variant="secondary" loading={retrying} onClick={() => void retry()}>
                Retry
              </Button>
            </p>
          )}
        </div>
      )}
      {!failedFirstLoad && (
        <>
          <StatusPanel items={items} host={hostSnapshot} history={history} lastEventIso={recentEvents[0]?.time} showActions={items.length > 0} />
          {degraded && (
            <Banner kind="warning" title="Degraded">
              Container status is unavailable, so every stack shows as unknown.
            </Banner>
          )}
          {items.length === 0 ? (
            <EmptyState title="Create your first stack" description="Create a stack or import an existing compose file.">
              <p className={styles.links}>
                <Link to="/stacks/new" className={styles.link} data-primary="true">
                  New stack
                </Link>
                <Link to="/stacks/import" className={styles.link}>
                  Import
                </Link>
              </p>
            </EmptyState>
          ) : (
            <div className={styles.columns}>
              <section className={styles.listCard}>
                <header className={styles.listHead}>
                  <h2 id="stacks-heading" className={styles.listTitle}>
                    Stacks
                  </h2>
                  <span className={styles.count}>{visible.length === items.length ? items.length : `${visible.length} of ${items.length}`}</span>
                  <label className={styles.search}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
                      <path d={ICONS.search} />
                    </svg>
                    <input type="search" aria-label="Search stacks" placeholder="Filter by name, project or path" value={query} onChange={(e) => setQuery(e.target.value)} />
                  </label>
                </header>
              </section>
            </div>
          )}
        </>
      )}
    </section>
  );
}
