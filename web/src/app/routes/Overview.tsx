import { useState } from 'react';
import { Link } from 'react-router';
import { issueCards } from '$lib/dashboard/issues';
import { host, hostHistory } from '$lib/stores/host';
import { events } from '$lib/stores/live';
import { runningOps, runningStackNames } from '$lib/stores/runningOps';
import { refreshStacks, stacks } from '$lib/stores/stacks';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useDashboard } from '../hooks/useDashboard';
import { useStore } from '../hooks/useStore';
import { IssueCards } from '../overview/IssueCards';
import { StacksList } from '../overview/StacksList';
import { StatusPanel } from '../overview/StatusPanel';
import { useStackAction } from '../overview/useStackAction';
import styles from './Overview.module.css';

/** Health first: the status panel, then the stack list. Eager: the only screen in the entry chunk. */
export default function Overview() {
  const { items, loading, error } = useStore(stacks);
  const hostSnapshot = useStore(host);
  const history = useStore(hostHistory);
  const recentEvents = useStore(events);
  // Alerts, summary and storage feed the issue cards and the side column; the controller runs while the screen is mounted.
  const { alerts, summary, storage, refreshAlerts } = useDashboard();
  const running = useStore(runningOps);
  const { act, acting, reasonFor } = useStackAction();
  const [query, setQuery] = useState('');
  const [retrying, setRetrying] = useState(false);

  async function actOnIssue(name: string, action: 'up' | 'restart') {
    // Failures are toasted by act(); only a started operation changes what the alerts say.
    if (await act(name, action)) refreshAlerts();
  }

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
          <IssueCards
            cards={issueCards({ alerts, storage })}
            stackStatus={(name) => items.find((s) => s.name === name)?.status}
            reasonFor={reasonFor}
            acting={acting}
            onAct={(name, action) => void actOnIssue(name, action)}
          />
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
              <StacksList
                items={items}
                alerts={alerts}
                summary={summary}
                query={query}
                onQuery={setQuery}
                runningStacks={runningStackNames(running)}
                reasonFor={(s) => reasonFor(s.name, s.status)}
                acting={acting}
                onAct={(name, action) => void act(name, action)}
              />
            </div>
          )}
        </>
      )}
    </section>
  );
}
