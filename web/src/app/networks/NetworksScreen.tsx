import { useMemo, useRef, useState } from 'react';
import { api } from '$lib/api/client';
import type { Network } from '$lib/api/types';
import { networkSummary, sortNetworks, summaryParts } from '$lib/networks/networks';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { CreateNetworkDialog } from './CreateNetworkDialog';
import { NetworkCard } from './NetworkCard';
import styles from './NetworksScreen.module.css';
import { useNetworkList } from './useNetworkList';

export function NetworksScreen() {
  const { rows, error, refresh } = useNetworkList();
  const networks = useMemo(() => sortNetworks(rows ?? []), [rows]);
  const parts = useMemo(() => summaryParts(networkSummary(rows ?? [])), [rows]);
  const [creating, setCreating] = useState(false);
  const [pending, setPending] = useState<Network>();
  const [removing, setRemoving] = useState<string>();
  // State updates are asynchronous, so a second synchronous click would still see no removal in flight.
  const removingRef = useRef(false);

  async function remove() {
    if (!pending || removingRef.current) return;
    const network = pending;
    removingRef.current = true;
    setPending(undefined);
    setRemoving(network.id);
    try {
      await api.deleteNetwork(network.id);
      toasts.push({ kind: 'success', message: `Network \`${network.name}\` removed` });
    } catch (err) {
      toasts.push({ kind: 'danger', message: err instanceof Error ? err.message : String(err) });
    } finally {
      removingRef.current = false;
      setRemoving(undefined);
      refresh();
    }
  }

  return (
    <>
      <section className={styles.page} aria-labelledby="networks-heading">
        <header className={styles.head}>
          <div className={styles.headText}>
            <h1 id="networks-heading">Networks</h1>
            {rows && (
              <p className={styles.lead}>
                <span>{parts[0]}</span>
                {parts.slice(1).map((part) => ` · ${part}`)}
              </p>
            )}
          </div>
          <Button variant="primary" onClick={() => setCreating(true)}>
            Create network
          </Button>
        </header>
        {error && !rows ? (
          <>
            <Banner kind="danger" title="Could not load networks">
              {error}
            </Banner>
            <p>
              <Button variant="secondary" onClick={refresh}>
                Retry
              </Button>
            </p>
          </>
        ) : !rows ? (
          <div className={`${styles.card} ${styles.pad}`}>
            <Spinner label="Loading networks" />
          </div>
        ) : networks.length === 0 ? (
          <EmptyState title="No networks" description="Networks appear here as soon as Docker creates them." />
        ) : (
          <div className={styles.grid}>
            {networks.map((n) => (
              <NetworkCard key={n.id} network={n} removing={removing === n.id} busy={removing !== undefined} onRemove={() => setPending(n)} />
            ))}
          </div>
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
      <ConfirmDialog
        open={pending !== undefined}
        title={`Remove network \`${pending?.name ?? ''}\`?`}
        message="Docker deletes the network. Containers can only attach again once it is recreated."
        confirmLabel="Remove"
        danger
        onConfirm={() => void remove()}
        onCancel={() => setPending(undefined)}
      />
    </>
  );
}
