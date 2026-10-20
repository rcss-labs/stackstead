import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, ApiError, createApi } from '$lib/api/client';
import type { Action, DockerEvent, FleetServer, FleetStack, Operation, StackSummary } from '$lib/api/types';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { createFleet, serverPath } from '$lib/servers/fleet';
import { combineHosts, type HostInput } from '$lib/servers/fleetHost';
import { createFleetStats } from '$lib/servers/fleetStats';
import { createOperationTracker } from '$lib/stores/operationTracker';
import { createRunningOpsStore } from '$lib/stores/runningOps';
import { writable } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Chip } from '../components/Chip';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import SideColumn from '../overview/SideColumn';
import { StacksList } from '../overview/StacksList';
import { StatusPanel } from '../overview/StatusPanel';
import { useServer } from '../servers/ServerContext';
import styles from './AllStacks.module.css';
import overview from './Overview.module.css';

const NO_STACKS: ReadonlySet<string> = new Set();
// A connected server's live state is unknown from here, so only its stack status and busy state gate actions.
const PEER_LIVE = { compose: '', docker: 'ok', events: 'connected' } as const;

/** One connected server's operation watcher: its running list feeds a tracker that resolves the toasts. */
interface PeerWatch {
  api: ReturnType<typeof createApi>;
  tracker: ReturnType<typeof createOperationTracker>;
  recheck: () => void;
  stop: () => void;
}

function startPeerWatch(server: FleetServer, onRunning: (ops: Operation[]) => void): PeerWatch {
  const peerApi = createApi(`/api/servers/${encodeURIComponent(server.id)}`);
  const store = createRunningOpsStore();
  const refresh = writable(0);
  const tracker = createOperationTracker({
    href: (path) => serverPath(server.slug, path),
    stoppedMessage: `Still running on ${server.name}`,
  });
  const stopOps = store.startRunningOps({ list: peerApi.runningOperations, changes: writable(null), refresh });
  const stopTracker = tracker.startOperationTracker({ runningOps: store.runningOps, get: peerApi.operation });
  const unsubscribe = store.runningOps.subscribe(onRunning);
  return {
    api: peerApi,
    tracker,
    recheck: () => refresh.update((n) => n + 1),
    stop: () => {
      unsubscribe();
      stopTracker();
      stopOps();
    },
  };
}

const peerApis = new Map<string, ReturnType<typeof createApi>>();
const peerApi = (id: string) => {
  let client = peerApis.get(id);
  if (!client) {
    client = createApi(`/api/servers/${encodeURIComponent(id)}`);
    peerApis.set(id, client);
  }
  return client;
};

const displayVersion = (v: string) => (/^v?\d/.test(v) ? `v${v.replace(/^v/, '')}` : v);

// The list components are typed on StackSummary; every item here came from the fleet and carries its server.
const fleetStack = (s: StackSummary) => s as FleetStack;
const rowKey = (s: StackSummary) => `${fleetStack(s).server}/${s.name}`;

/** The Overview, fed by every connected server at once. A server that does not answer contributes a banner, never stale rows. */
export default function AllStacks() {
  const scope = useServer();
  const fleet = useMemo(() => createFleet({ load: api.fleetStacks }), []);
  const { data, loading, error } = useStore(fleet.state);
  const localIdRef = useRef<string | undefined>(undefined);
  const fleetStats = useMemo(() => {
    const clientFor = (id: string) => (id === localIdRef.current ? api : peerApi(id));
    return createFleetStats({ loadSummary: (id) => clientFor(id).metricsSummary(), loadHost: (id) => clientFor(id).host() });
  }, []);
  const stats = useStore(fleetStats.state);
  const liveState = useStore(scope.live);
  const localOps = useStore(scope.runningOps);
  const [selected, setSelected] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [acting, setActing] = useState<ReadonlySet<string>>(NO_STACKS);
  const inFlight = useRef(new Set<string>());
  const [peerOps, setPeerOps] = useState<Record<string, Operation[]>>({});
  const watches = useRef(new Map<string, PeerWatch>());

  useEffect(() => {
    fleet.start();
    return () => fleet.stop();
  }, [fleet]);

  useEffect(() => {
    fleetStats.start();
    return () => fleetStats.stop();
  }, [fleetStats]);

  // Trackers live as long as the screen so a toast keeps resolving after the action; leaving settles the pending ones.
  useEffect(() => {
    const open = watches.current;
    return () => {
      for (const watch of open.values()) watch.stop();
      open.clear();
    };
  }, []);

  const servers = useMemo(() => data?.servers ?? [], [data]);
  const byId = useMemo(() => new Map(servers.map((s) => [s.id, s])), [servers]);
  const answering = servers.filter((s) => s.status === 'reachable');
  const failing = servers.filter((s) => s.status !== 'reachable');
  const localServer = servers.find((s) => s.local);
  localIdRef.current = localServer?.id;

  const answeringKey = answering.map((s) => s.id).join('\n');
  useEffect(() => {
    fleetStats.setServers(answeringKey === '' ? [] : answeringKey.split('\n'));
  }, [fleetStats, answeringKey]);

  // A server that has not answered yet is neither counted nor listed; one that failed or is unreachable is named.
  const hostInputs: HostInput[] = servers.flatMap((server): HostInput[] => {
    if (server.status !== 'reachable') return [{ name: server.name, reason: server.status === 'incompatible' ? 'incompatible version' : 'unreachable' }];
    const entry = stats[server.id];
    if (!entry) return [];
    return [{ name: server.name, host: entry.host, reason: 'unavailable' }];
  });
  const { host: combinedHost, notes: hostNotes } = combineHosts(hostInputs);

  const slugOf = useCallback(
    (id: string) => {
      const server = byId.get(id);
      return server && !server.local ? server.slug : null;
    },
    [byId],
  );

  const watchFor = useCallback((server: FleetServer): PeerWatch => {
    let watch = watches.current.get(server.id);
    if (!watch) {
      watch = startPeerWatch(server, (ops) => setPeerOps((prev) => (prev[server.id] === ops ? prev : { ...prev, [server.id]: ops })));
      watches.current.set(server.id, watch);
    }
    return watch;
  }, []);

  // Only servers that answered contribute rows; a failing server's last data would be stale.
  const combined = (data?.stacks ?? []).filter((s) => byId.get(s.server)?.status === 'reachable');
  const activeServer = selected !== null && answering.some((s) => s.id === selected) ? selected : null;
  const shown = activeServer === null ? combined : combined.filter((s) => s.server === activeServer);
  const events = (data?.events ?? []).filter((e) => byId.get(e.server)?.status === 'reachable');
  const countOf = (id: string) => combined.filter((s) => s.server === id).length;

  const runningKeys = useMemo(() => {
    const keys = new Set<string>();
    if (localServer) for (const o of localOps) keys.add(`${localServer.id}/${o.stack}`);
    for (const [id, ops] of Object.entries(peerOps)) for (const o of ops) keys.add(`${id}/${o.stack}`);
    return keys;
  }, [localOps, peerOps, localServer]);

  const act = useCallback(
    async (stack: FleetStack, action: Action) => {
      const key = rowKey(stack);
      if (inFlight.current.has(key)) return;
      inFlight.current.add(key);
      setActing(new Set(inFlight.current));
      try {
        const server = byId.get(stack.server);
        if (!server || server.local) {
          scope.trackOperation(await scope.api.act(stack.name, action));
          scope.refreshRunningOps();
        } else {
          const watch = watchFor(server);
          watch.tracker.trackOperation(await watch.api.act(stack.name, action));
          watch.recheck();
        }
      } catch (e) {
        const message = e instanceof Error ? e.message : String(e);
        toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: `Could not run ${action}: ${message}` });
      } finally {
        inFlight.current.delete(key);
        setActing(new Set(inFlight.current));
      }
    },
    [byId, scope, watchFor],
  );

  const reasonFor = (s: StackSummary) => {
    const key = rowKey(s);
    const busy = runningKeys.has(key) || acting.has(key);
    return lifecycleDisabledReason({ live: byId.get(fleetStack(s).server)?.local ? liveState : PEER_LIVE, status: s.status, busy });
  };

  const serverOf = (s: { server: string }) => ({ name: byId.get(s.server)?.name ?? s.server, slug: slugOf(s.server) });
  const stackHref = (s: StackSummary) => serverPath(slugOf(fleetStack(s).server), `/stacks/${encodeURIComponent(s.name)}`);

  if (loading && !data) {
    return (
      <section className={overview.page}>
        <div className={overview.loading}>
          <Spinner />
        </div>
      </section>
    );
  }

  return (
    <section className={overview.page}>
      {error && (
        <div>
          <Banner kind="danger" title="Could not load stacks">
            {error}
          </Banner>
          {!data && (
            <p>
              <Button variant="secondary" onClick={() => void fleet.refresh()}>
                Retry
              </Button>
            </p>
          )}
        </div>
      )}
      {failing.map((server) => (
        <ServerBanner key={server.id} server={server} selfVersion={localServer?.version ?? ''} />
      ))}
      {data && (combined.length > 0 || failing.length === 0) && (
        <>
          <StatusPanel items={combined} host={combinedHost} history={[]} lastEventIso={events[0]?.time} showActions={false} hrefOf={stackHref} keyOf={rowKey} />
          {hostNotes.length > 0 && <p className={styles.hostNote}>{hostNotes.join(' · ')}</p>}
          {combined.length === 0 ? (
            <EmptyState title="No stacks yet" description="None of the connected servers has a stack." />
          ) : (
            <>
              <div className={styles.chips} role="group" aria-label="Filter by server">
                <Chip pressed={activeServer === null} count={combined.length} onClick={() => setSelected(null)}>
                  All
                </Chip>
                {answering.map((s) => (
                  <Chip key={s.id} pressed={activeServer === s.id} count={countOf(s.id)} onClick={() => setSelected(s.id)}>
                    {s.name}
                  </Chip>
                ))}
              </div>
              <div className={overview.columns}>
                <StacksList
                  items={shown}
                  alerts={undefined}
                  summary={undefined}
                  usageOf={(s) => stats[fleetStack(s).server]?.summary?.stacks[s.project]}
                  query={query}
                  onQuery={setQuery}
                  runningStacks={runningKeys}
                  reasonFor={reasonFor}
                  acting={acting}
                  onAct={(_, action, stack) => void act(fleetStack(stack), action)}
                  serverOf={(s) => serverOf(fleetStack(s))}
                  hrefOf={stackHref}
                  keyOf={rowKey}
                />
                <SideColumn
                  events={events}
                  connection={error ? 'reconnecting' : 'open'}
                  top={undefined}
                  stackNames={{}}
                  hideTop
                  serverOf={(e: DockerEvent) => serverOf(e as DockerEvent & { server: string })}
                />
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}

function ServerBanner({ server, selfVersion }: { server: FleetServer; selfVersion: string }) {
  if (server.status === 'incompatible') {
    const runs = server.version ? `runs ${displayVersion(server.version)}` : 'runs a different version';
    const self = server.version && selfVersion ? `, this server ${displayVersion(selfVersion)}` : '';
    return <Banner kind="danger" title={`${server.name} ${runs}${self}. Update to match`} />;
  }
  return (
    <Banner kind="danger" title={`Can't reach ${server.name}`}>
      {server.error || undefined}
    </Banner>
  );
}
