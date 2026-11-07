import { Suspense, lazy, useEffect, useState } from 'react';
import { headline } from '$lib/dashboard/overview';
import { issueCards } from '$lib/dashboard/issues';
import { runningStackNames } from '$lib/stores/runningOps';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Spinner } from '../components/Spinner';
import { useDashboard } from '../hooks/useDashboard';
import { useStore } from '../hooks/useStore';
import { IssueCards } from '../overview/IssueCards';
import SideColumn from '../overview/SideColumn';
import { StacksList } from '../overview/StacksList';
import { StatusPanel } from '../overview/StatusPanel';
import { useStackAction } from '../overview/useStackAction';
import styles from './Overview.module.css';

import { FEATURE_DISK_FORECAST, useServer } from '../servers/ServerContext';

// Shown once, with no stacks; kept out of the entry chunk.
const FirstRun = lazy(() => import('../overview/FirstRun').then((m) => ({ default: m.FirstRun })));

/** Days until the disk fills, when this server can forecast it and has enough history to; otherwise undefined and nothing is drawn. */
function useDiskForecast(): number | undefined {
  const { api, features } = useServer();
  const supported = features.includes(FEATURE_DISK_FORECAST);
  const [days, setDays] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!supported) return;
    let current = true;
    api
      .diskForecast()
      .then((f) => current && setDays(f.daysLeft ?? undefined))
      .catch(() => current && setDays(undefined));
    return () => {
      current = false;
    };
  }, [api, supported]);
  return supported ? days : undefined;
}

/** Health first: the headline, then what needs attention, then the stack list. Eager: the only screen in the entry chunk. */
export default function Overview() {
  const { stacks, host, hostHistory, events, live, runningOps, refreshStacks, refreshStatus } = useServer();
  const { items, loading, error } = useStore(stacks);
  const hostSnapshot = useStore(host);
  const history = useStore(hostHistory);
  const recentEvents = useStore(events);
  const liveState = useStore(live);
  const { connection } = liveState;
  const forecastDays = useDiskForecast();
  // Alerts, summary and storage feed the issue cards and the side column; the controller runs while the screen is mounted.
  const { alerts, summary, storage } = useDashboard();
  const running = useStore(runningOps);
  const { act, acting, reasonFor } = useStackAction();
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
  const stackNames = Object.fromEntries(items.map((s) => [s.project, s.name]));
  const degraded = items.length > 0 && items.every((s) => s.status === 'unknown');
  const dockerDown = liveState.docker === 'unavailable';
  // Welcome only while Docker answers: with Docker down, "no stacks" may just mean none can be seen.
  const firstRun = items.length === 0 && !dockerDown;

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
      {!failedFirstLoad && firstRun && (
        <Suspense
          fallback={
            <div className={styles.loading}>
              <Spinner />
            </div>
          }
        >
          <FirstRun host={hostSnapshot} dockerVersion={liveState.dockerVersion} />
        </Suspense>
      )}
      {!failedFirstLoad && !firstRun && (
        <>
          <StatusPanel
            items={items}
            headline={headline(items, alerts, liveState, hostSnapshot)}
            host={hostSnapshot}
            history={history}
            lastEventIso={dockerDown ? undefined : recentEvents[0]?.time}
            showActions={items.length > 0}
            forecastDays={forecastDays}
          />
          {dockerDown && (
            <Banner kind="danger" title="Cannot reach Docker">
              Showing the last known state. Actions come back when Docker reconnects.
              <p>
                <Button variant="secondary" size="sm" onClick={() => void refreshStatus()}>
                  Retry now
                </Button>
              </p>
            </Banner>
          )}
          {degraded && (
            <Banner kind="warning" title="Degraded">
              Container status is unavailable, so every stack shows as unknown.
            </Banner>
          )}
          {/* Alerts come from the same Docker view, so while Docker is down they would be stale. */}
          <IssueCards
            cards={dockerDown ? undefined : issueCards({ alerts, storage })}
            stackStatus={(name) => items.find((s) => s.name === name)?.status}
            reasonFor={reasonFor}
            acting={acting}
            onAct={(name, action) => void act(name, action)}
          />
          <div className={styles.columns}>
            {items.length === 0 ? null : (
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
            )}
            <SideColumn events={recentEvents} connection={connection} top={summary?.top} stackNames={stackNames} />
          </div>
        </>
      )}
    </section>
  );
}
