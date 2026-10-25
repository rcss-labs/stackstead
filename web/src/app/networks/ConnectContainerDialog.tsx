import { useEffect, useState, type FormEvent } from 'react';
import type { ContainerRow, Network } from '$lib/api/types';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { Spinner } from '../components/Spinner';
import { useServer } from '../servers/ServerContext';
import styles from './CreateNetworkDialog.module.css';

export interface ConnectContainerDialogProps {
  network: Network | undefined;
  onClose: () => void;
  onConnected: (container: string) => void;
}

export function ConnectContainerDialog({ network, onClose, onConnected }: ConnectContainerDialogProps) {
  const { api } = useServer();
  const open = network !== undefined;
  const [candidates, setCandidates] = useState<ContainerRow[]>();
  const [choice, setChoice] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!network) return;
    let current = true;
    setCandidates(undefined);
    setChoice('');
    setError('');
    api.containers().then(
      (rows) => {
        if (!current) return;
        const attached = new Set(network.containers.map((c) => c.name));
        const free = rows.filter((r) => !attached.has(r.name)).sort((a, b) => a.name.localeCompare(b.name));
        setCandidates(free);
        setChoice(free[0]?.name ?? '');
      },
      (err: unknown) => {
        if (!current) return;
        setCandidates([]);
        setError(err instanceof Error ? err.message : String(err));
      },
    );
    return () => {
      current = false;
    };
  }, [api, network]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!network || !choice || busy) return;
    setBusy(true);
    setError('');
    try {
      await api.connectNetwork(network.id, choice);
      onConnected(choice);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} title={`Connect a container to \`${network?.name ?? ''}\``} onClose={onClose}>
      <form className={styles.form} onSubmit={(e) => void submit(e)}>
        {error && <Banner kind="danger" title={error} />}
        {!candidates ? (
          <Spinner label="Loading containers" />
        ) : candidates.length === 0 ? (
          <p>Every container is already on this network.</p>
        ) : (
          <Field label="Container" id="connect-container" mono>
            {({ readOnly: _, ...c }) => (
              <select {...c} value={choice} onChange={(e) => setChoice(e.target.value)}>
                {candidates.map((r) => (
                  <option key={r.id} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
        )}
        <div className={styles.actions}>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={busy} disabledReason={choice ? undefined : 'Pick a container'}>
            Connect
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
