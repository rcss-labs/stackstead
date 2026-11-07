import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import type { Network, NetworkContainer } from '$lib/api/types';
import { containerKey, networkSummary, sortNetworks, summaryParts, type NetworkFilter } from '$lib/networks/networks';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { CopyButton } from '../components/CopyButton';
import { Dialog } from '../components/Dialog';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { Tabs } from '../components/Tabs';
import { useIsMobile } from '../hooks/useIsMobile';
import { useServer } from '../servers/ServerContext';
import { ConnectContainerDialog } from './ConnectContainerDialog';
import { CreateNetworkDialog } from './CreateNetworkDialog';
import { NetworkDetail } from './NetworkDetail';
import { NetworkListPane } from './NetworkListPane';
import styles from './NetworksScreen.module.css';
import { useNetworkList } from './useNetworkList';

type Pending = { kind: 'remove'; network: Network } | { kind: 'detach'; network: Network; container: NetworkContainer } | { kind: 'prune' };

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

export interface NetworksScreenProps {
  /** The selected network's name, from the URL. */
  selected?: string;
}

export function NetworksScreen({ selected }: NetworksScreenProps) {
  const { api, href } = useServer();
  const navigate = useNavigate();
  const { rows, error, refresh } = useNetworkList();
  const networks = useMemo(() => sortNetworks(rows ?? []), [rows]);
  const parts = summaryParts(networkSummary(networks));
  const [filter, setFilter] = useState<NetworkFilter>('all');
  const [query, setQuery] = useState('');
  // Keyed by network so a new selection starts without a highlight.
  const [highlight, setHighlight] = useState<{ network: string; key: string }>();
  const [creating, setCreating] = useState(false);
  const [inspecting, setInspecting] = useState(false);
  const [connecting, setConnecting] = useState<Network>();
  const [pending, setPending] = useState<Pending>();
  const [working, setWorking] = useState(false);
  // State updates are asynchronous, so a second synchronous click would still see no change in flight.
  const workingRef = useRef(false);

  const current = selected === undefined ? undefined : networks.find((n) => n.name === selected);
  const highlighted = current && highlight?.network === current.name ? highlight.key : null;

  // On a phone the list and a network's detail are separate screens; the shell's top bar goes back to the list.
  const mobile = useIsMobile();
  const showList = !mobile || selected === undefined;
  const showDetail = !mobile || selected !== undefined;
  const showHeader = showList;
  // Set by a removal that navigates away from the detail; consumed once the heading is on screen.
  const focusHeading = useRef(false);
  useEffect(() => {
    if (!focusHeading.current || !showHeader) return;
    focusHeading.current = false;
    document.getElementById('networks-heading')?.focus();
  }, [showHeader, selected]);

  const tabs = [
    { id: 'networks', label: 'Networks', badge: rows ? networks.length : undefined },
    { id: 'ports', label: 'Ports' },
  ];
  function selectTab(id: string) {
    if (id === 'ports') void navigate(href('/ports'));
  }

  function select(network: Network) {
    setHighlight(undefined);
    void navigate(href(`/networks/${encodeURIComponent(network.name)}`));
  }

  function toggle(container: NetworkContainer) {
    if (!current) return;
    const key = containerKey(container);
    setHighlight(highlighted === key ? undefined : { network: current.name, key });
  }

  async function run(action: Pending) {
    if (workingRef.current) return;
    workingRef.current = true;
    setWorking(true);
    try {
      if (action.kind === 'remove') {
        await api.deleteNetwork(action.network.id);
        toasts.push({ kind: 'success', message: `Network \`${action.network.name}\` removed` });
        // The heading is not rendered on a phone's detail screen, so focus waits for the list to show.
        focusHeading.current = true;
        void navigate(href('/networks'));
      } else if (action.kind === 'detach') {
        await api.disconnectNetwork(action.network.id, action.container.name);
        toasts.push({ kind: 'success', message: `Detached \`${action.container.name}\` from \`${action.network.name}\`` });
      } else {
        const { removed } = await api.pruneNetworks();
        toasts.push({
          kind: 'success',
          message: removed.length ? `Removed ${removed.length} unused network${removed.length === 1 ? '' : 's'}` : 'No unused networks to remove',
        });
        if (selected !== undefined && removed.includes(selected)) void navigate(href('/networks'));
      }
    } catch (err) {
      toasts.push({ kind: 'danger', message: message(err) });
    } finally {
      workingRef.current = false;
      setWorking(false);
      setPending(undefined);
      refresh();
    }
  }

  const confirm =
    pending?.kind === 'remove'
      ? { title: `Remove network \`${pending.network.name}\`?`, message: 'Docker deletes the network. Containers can only attach again once it is recreated.', label: 'Remove' }
      : pending?.kind === 'detach'
        ? { title: `Detach \`${pending.container.name}\`?`, message: `The container loses its address on \`${pending.network.name}\` until it is connected again or recreated.`, label: 'Detach' }
        : { title: 'Prune unused networks?', message: 'Docker removes every network that no container uses. Built-in networks are kept.', label: 'Prune' };

  let detail;
  if (selected === undefined) {
    detail = (
      <div className={styles.state}>
        <EmptyState title="Select a network" description="Pick a network from the list to see its configuration and topology." />
      </div>
    );
  } else if (!current) {
    detail = (
      <div className={styles.state}>
        <EmptyState title="Network not found" description={`No network named ${selected} exists on this server.`} />
      </div>
    );
  } else {
    detail = (
      <NetworkDetail
        network={current}
        highlighted={highlighted}
        busy={working}
        onToggle={toggle}
        onInspect={() => setInspecting(true)}
        onConnect={() => setConnecting(current)}
        onRemove={() => setPending({ kind: 'remove', network: current })}
        onDetach={(container) => setPending({ kind: 'detach', network: current, container })}
      />
    );
  }

  const content =
    error && !rows ? (
      <div className={styles.state}>
        <Banner kind="danger" title="Could not load networks">
          {error}
        </Banner>
        <p>
          <Button variant="secondary" onClick={refresh}>
            Retry
          </Button>
        </p>
      </div>
    ) : !rows ? (
      <div className={styles.state}>
        <Spinner label="Loading networks" />
      </div>
    ) : (
      <div className={styles.body}>
        {showList && (
          <div className={styles.listPane}>
            <NetworkListPane networks={networks} selected={current?.name} filter={filter} query={query} onFilter={setFilter} onQuery={setQuery} onSelect={select} />
          </div>
        )}
        {showDetail && <div aria-live="polite">{detail}</div>}
      </div>
    );

  return (
    <>
      <section className={styles.page} aria-labelledby={showHeader ? 'networks-heading' : undefined} aria-label={showHeader ? undefined : 'Network detail'}>
        {showHeader && (
          <header className={styles.head}>
            <div className={styles.headText}>
              <h1 id="networks-heading" tabIndex={-1}>
                Networks
              </h1>
              {rows && <p className={styles.lead}>{parts.join(' · ')}</p>}
            </div>
            <div className={styles.headActions}>
              <Button variant="secondary" size="sm" disabledReason={working ? 'Another change is in progress' : undefined} onClick={() => setPending({ kind: 'prune' })}>
                Prune unused
              </Button>
              <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
                Create network
              </Button>
            </div>
          </header>
        )}
        {showHeader ? (
          <div className={styles.frame}>
            <Tabs tabs={tabs} selected="networks" ariaLabel="Network sections" onSelect={selectTab}>
              {() => content}
            </Tabs>
          </div>
        ) : (
          content
        )}
      </section>
      <CreateNetworkDialog
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(network) => {
          toasts.push({ kind: 'success', message: `Network \`${network.name}\` created` });
          refresh();
        }}
      />
      <ConnectContainerDialog
        network={connecting}
        onClose={() => setConnecting(undefined)}
        onConnected={(container) => {
          toasts.push({ kind: 'success', message: `Connected \`${container}\` to \`${connecting?.name ?? ''}\`` });
          refresh();
        }}
      />
      <Dialog open={inspecting && current !== undefined} title={`Inspect \`${current?.name ?? ''}\``} width="lg" onClose={() => setInspecting(false)}>
        <pre className={styles.inspect}>{JSON.stringify(current, null, 2)}</pre>
        <div className={styles.inspectActions}>
          <CopyButton text={JSON.stringify(current, null, 2)} label="Copy JSON" size="sm" />
          <Button variant="secondary" onClick={() => setInspecting(false)}>
            Close
          </Button>
        </div>
      </Dialog>
      <ConfirmDialog
        open={pending !== undefined}
        title={confirm.title}
        message={confirm.message}
        confirmLabel={confirm.label}
        danger
        pending={working}
        onConfirm={() => pending && void run(pending)}
        onCancel={() => !working && setPending(undefined)}
      />
    </>
  );
}
