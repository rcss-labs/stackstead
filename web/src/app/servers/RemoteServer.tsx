import { Suspense, lazy, useEffect, useMemo, useState } from 'react';
import { Route, Routes, useParams } from 'react-router';
import { api, createApi } from '$lib/api/client';
import type { PeerView } from '$lib/api/types';
import { relativeTime } from '$lib/format';
import { peers, refreshPeers } from '$lib/servers/peers';
import { scopedOpen } from '$lib/sse/client';
import { createHostStore } from '$lib/stores/host';
import { createLiveStore } from '$lib/stores/live';
import { createOperationTracker } from '$lib/stores/operationTracker';
import { createRunningOpsStore } from '$lib/stores/runningOps';
import { createStacksStore } from '$lib/stores/stacks';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import { SERVER_ROUTES } from '../routes';
import { ServerContext } from './ServerContext';
import type { ServerScope } from './ServerContext';
import styles from './RemoteServer.module.css';

const NotFound = lazy(() => import('../routes/NotFound'));

/** The screens of `/s/:server/*`: the same routes as this server, fed by a connected server. */
export default function RemoteServer() {
  const { server: slug } = useParams();
  const { items, loaded } = useStore(peers);
  const peer = items.find((p) => p.slug === slug);
  if (!loaded) return <Spinner label="Loading server" />;
  if (!peer) return <NotFoundPage />;
  // Keyed so another server never inherits this one's stores and streams.
  return <ConnectedServer key={peer.id} peer={peer} />;
}

function NotFoundPage() {
  return (
    <Suspense fallback={<Spinner label="Loading page" />}>
      <NotFound />
    </Suspense>
  );
}

/** One scope per mounted server: its own stores and streams, stopped when the user leaves. */
function createScope(peer: Pick<PeerView, 'id' | 'slug' | 'name'>) {
  const base = `/api/servers/${encodeURIComponent(peer.id)}`;
  const href = (path: string) => `/s/${encodeURIComponent(peer.slug)}${path === '/' ? '' : path}`;
  const scopedApi = createApi(base);
  const open = scopedOpen(base);
  const liveStore = createLiveStore();
  const stacksStore = createStacksStore();
  const hostStore = createHostStore();
  const opsStore = createRunningOpsStore();
  const tracker = createOperationTracker({ href });

  const scope: ServerScope = {
    local: false,
    id: peer.id,
    slug: peer.slug,
    name: peer.name,
    api: scopedApi,
    open,
    live: liveStore.live,
    changes: liveStore.changes,
    events: liveStore.events,
    refreshStatus: liveStore.refreshStatus,
    stacks: stacksStore.stacks,
    refreshStacks: stacksStore.refreshStacks,
    host: hostStore.host,
    hostHistory: hostStore.hostHistory,
    runningOps: opsStore.runningOps,
    refreshRunningOps: stacksStore.refreshRunningOps,
    trackOperation: tracker.trackOperation,
    href,
  };

  function start(): () => void {
    liveStore.startLive({ fetchStatus: scopedApi.status, open });
    const stopStacks = stacksStore.startStacks({ list: scopedApi.stacks, changes: liveStore.changes });
    const releaseHost = hostStore.startHost({ host: scopedApi.host });
    const stopOps = opsStore.startRunningOps({
      list: scopedApi.runningOperations,
      changes: liveStore.changes,
      refresh: stacksStore.operationsRefresh,
    });
    const stopTracker = tracker.startOperationTracker({ runningOps: opsStore.runningOps, get: scopedApi.operation });
    return () => {
      stopTracker();
      stopOps();
      releaseHost();
      stopStacks();
      liveStore.stopLive();
    };
  }

  return { scope, start };
}

function ConnectedServer({ peer }: { peer: PeerView }) {
  const { scope, start } = useMemo(() => createScope(peer), [peer.id, peer.slug]); // eslint-disable-line react-hooks/exhaustive-deps
  const value = useMemo(() => ({ ...scope, name: peer.name }), [scope, peer.name]);
  const reachable = peer.status === 'reachable';

  // Streams run only while the server answers, and always stop when the user leaves it.
  useEffect(() => (reachable ? start() : undefined), [reachable, start]);

  if (!reachable) return <PeerState peer={peer} />;
  return (
    <ServerContext.Provider value={value}>
      <Suspense fallback={<Spinner label="Loading page" />}>
        <Routes>
          {SERVER_ROUTES.map(({ path, screen: Screen }) =>
            path === '' ? <Route key="index" index element={<Screen />} /> : <Route key={path} path={path} element={<Screen />} />,
          )}
          <Route path="*" element={<NotFound />} />
        </Routes>
      </Suspense>
    </ServerContext.Provider>
  );
}

const stripV = (version: string) => version.replace(/^v/, '');

function PeerState({ peer }: { peer: PeerView }) {
  switch (peer.status) {
    case 'unreachable':
      return <Unreachable peer={peer} />;
    case 'incompatible':
      return <Incompatible peer={peer} />;
    case 'wrong_instance':
      return (
        <EmptyState
          title={`${peer.name} is a different server now`}
          description={`${peer.url} no longer answers as the server that was connected. Remove the connection and add it again.`}
        />
      );
    case 'removed':
      return <EmptyState title={`${peer.name} removed this connection`} />;
    default:
      return <Spinner label={`Connecting to ${peer.name}`} />;
  }
}

function Unreachable({ peer }: { peer: PeerView }) {
  const [retrying, setRetrying] = useState(false);
  const seen = peer.lastSeenAt ? relativeTime(peer.lastSeenAt, new Date()) : 'never';

  async function retry() {
    setRetrying(true);
    try {
      await api.checkPeer(peer.id);
    } catch {
      // The refreshed list below carries the outcome.
    }
    try {
      await refreshPeers();
    } finally {
      setRetrying(false);
    }
  }

  return (
    <div className={styles.state}>
      <p className={styles.message} role="status">{`Can't reach ${peer.name} (last seen ${seen}): ${peer.lastError}`}</p>
      <Button variant="secondary" loading={retrying} onClick={() => void retry()}>
        Retry
      </Button>
    </div>
  );
}

function Incompatible({ peer }: { peer: PeerView }) {
  const [self, setSelf] = useState<string>();
  useEffect(() => {
    let cancelled = false;
    api
      .serverSelf()
      .then((s) => !cancelled && setSelf(s.version))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);
  return (
    <div className={styles.state}>
      <p className={styles.message} role="status">
        {`${peer.name} runs v${stripV(peer.version)}, this server v${self ? stripV(self) : '?'}. Update to match`}
      </p>
    </div>
  );
}
