import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { api, ApiError, createApi } from '$lib/api/client';
import type { Action, DockerEvent, FleetServer, FleetStack, StackSummary } from '$lib/api/types';
import { BUSY_REASON } from '$lib/stacks/lifecycle';
import { createFleet, serverPath } from '$lib/servers/fleet';
import { peers } from '$lib/servers/peers';
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
const stripV = (version: string) => version.replace(/^v/, '');

// The list components are typed on StackSummary; every item here came from the fleet and carries its server.
const fleetStack = (s: StackSummary) => s as FleetStack;
const rowKey = (s: StackSummary) => `${fleetStack(s).server}/${s.name}`;

/** The Overview, fed by every connected server at once. A server that does not answer contributes a banner, never stale rows. */
export default function AllStacks() {
  const scope = useServer();
  const navigate = useNavigate();
  const fleet = useMemo(() => createFleet({ load: api.fleetStacks }), []);
  const { data, loading, error } = useStore(fleet.state);
  const known = useStore(peers).items;
  const [selected, setSelected] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [acting, setActing] = useState<ReadonlySet<string>>(NO_STACKS);
  const inFlight = useRef(new Set<string>());
  const [selfVersion, setSelfVersion] = useState<string>();

  useEffect(() => {
    fleet.start();
    return () => fleet.stop();
  }, [fleet]);

  const servers = data?.servers ?? [];
  const byId = useMemo(() => new Map(servers.map((s) => [s.id, s])), [servers]);
  const answering = servers.filter((s) => s.status === 'reachable');
  const failing = servers.filter((s) => s.status !== 'reachable');
  const hasIncompatible = failing.some((s) => s.status === 'incompatible');

  useEffect(() => {
    if (!hasIncompatible) return;
    let cancelled = false;
    api
      .serverSelf()
      .then((s) => !cancelled && setSelfVersion(s.version))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [hasIncompatible]);

  const slugOf = (id: string) => {
    const server = byId.get(id);
    return server && !server.local ? server.slug : null;
  };

  // Only servers that answered contribute rows; a failing server's last data would be stale.
  const combined = (data?.stacks ?? []).filter((s) => byId.get(s.server)?.status === 'reachable');
  const activeServer = selected !== null && answering.some((s) => s.id === selected) ? selected : null;
  const shown = activeServer === null ? combined : combined.filter((s) => s.server === activeServer);
  const events = (data?.events ?? []).filter((e) => byId.get(e.server)?.status === 'reachable');
  const countOf = (id: string) => combined.filter((s) => s.server === id).length;

  const act = useCallback(
    async (stack: FleetStack, action: Action) => {
      const key = rowKey(stack);
      if (inFlight.current.has(key)) return;
      inFlight.current.add(key);
      setActing(new Set(inFlight.current));
      try {
        const server = byId.get(stack.server);
        const isLocal = server?.local ?? false;
        const target = isLocal ? scope.api : createApi(`/api/servers/${encodeURIComponent(stack.server)}`);
        const op = await target.act(stack.name, action);
        // Only this server's tracker can resolve its operations; a connected server's progress is shown on its own screen.
        if (isLocal) {
          scope.trackOperation(op);
          scope.refreshRunningOps();
        }
        navigate(serverPath(slugOf(stack.server), `/stacks/${encodeURIComponent(stack.name)}?tab=operations&op=${encodeURIComponent(op.id)}`));
      } catch (e) {
        const message = e instanceof Error ? e.message : String(e);
        toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: `Could not run ${action}: ${message}` });
      } finally {
        inFlight.current.delete(key);
        setActing(new Set(inFlight.current));
      }
    },
    // slugOf reads byId only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [byId, navigate, scope],
  );

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
        <ServerBanner key={server.id} server={server} version={known.find((p) => p.id === server.id)?.version} selfVersion={selfVersion} />
      ))}
      {data && (
        <>
          <StatusPanel items={combined} host={undefined} history={[]} lastEventIso={events[0]?.time} showActions={false} hrefOf={stackHref} keyOf={rowKey} />
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
                  query={query}
                  onQuery={setQuery}
                  runningStacks={NO_STACKS}
                  reasonFor={(s) =>
                    s.status === 'unavailable' ? 'the compose file cannot be parsed' : acting.has(rowKey(s)) ? BUSY_REASON : undefined
                  }
                  acting={acting}
                  onAct={(_, action, stack) => void act(fleetStack(stack), action)}
                  serverOf={(s) => serverOf(fleetStack(s))}
                  hrefOf={stackHref}
                  keyOf={rowKey}
                />
                <SideColumn
                  events={events}
                  connection="open"
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

function ServerBanner({ server, version, selfVersion }: { server: FleetServer; version: string | undefined; selfVersion: string | undefined }) {
  if (server.status === 'incompatible') {
    return (
      <Banner kind="danger" title={`${server.name} runs ${version ? `v${stripV(version)}` : 'another version'}, this server ${selfVersion ? `v${stripV(selfVersion)}` : '?'}. Update to match`} />
    );
  }
  return (
    <Banner kind="danger" title={`Can't reach ${server.name}`}>
      {server.error || undefined}
    </Banner>
  );
}
