import { useEffect, useId, useRef, useState } from 'react';
import type { Network } from '$lib/api/types';
import { Field } from '../components/Field';
import { ListEditor } from '../components/ListEditor';
import { CreateNetworkDialog } from '../networks/CreateNetworkDialog';
import styles from './NetworkPicker.module.css';

import { useServer } from '../servers/ServerContext';
export interface NetworkAttachment {
  name: string;
  ipv4Address: string;
}

export interface NetworkPickerProps {
  value: NetworkAttachment[];
  onChange: (v: NetworkAttachment[]) => void;
  readonly?: boolean;
  errors?: Record<number, string>;
  /** Stable id prefix: controls get `${idPrefix}-{row}-name` and `-ip`. */
  idPrefix?: string;
}

interface PendingCreate {
  index: number;
  item: NetworkAttachment;
  set: (v: NetworkAttachment) => void;
}

const CREATE = '__create__';
const rename = (item: NetworkAttachment, name: string): NetworkAttachment => ({ name, ipv4Address: name ? item.ipv4Address : '' });
const label = (n: Network) => (n.subnets.length ? `${n.name} (${n.subnets.join(', ')})` : n.name);

export function NetworkPicker({ value, onChange, readonly = false, errors = {}, idPrefix }: NetworkPickerProps) {
  const { api } = useServer();
  const uid = useId();
  const prefix = idPrefix ?? uid;
  const [networks, setNetworks] = useState<Network[]>([]);
  const [failed, setFailed] = useState(false);
  const [creating, setCreating] = useState(false);
  // The row whose select shows "Create network…" until the dialog is cancelled or the new network is chosen.
  const [pending, setPending] = useState<PendingCreate | null>(null);
  const created = useRef(false);
  const alive = useRef(true);
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    alive.current = true;
    api.networks().then(
      (list) => alive.current && setNetworks(list),
      () => alive.current && setFailed(true),
    );
    return () => {
      alive.current = false;
    };
  }, [api]);

  function choose(name: string, item: NetworkAttachment, index: number, set: (v: NetworkAttachment) => void) {
    if (name !== CREATE) {
      set(rename(item, name));
      return;
    }
    created.current = false;
    setPending({ index, item, set });
    setCreating(true);
  }

  function closeCreate() {
    setCreating(false);
    if (!created.current) setPending(null);
  }

  async function networkCreated(network: Network) {
    created.current = true;
    const target = pending;
    let list: Network[] | undefined;
    try {
      list = await api.networks();
    } catch {
      // keep the known list and append the new network
    }
    if (!alive.current) return;
    setNetworks((known) => list ?? [...known, network]);
    if (target && valueRef.current[target.index] === target.item) target.set(rename(target.item, network.name));
    setPending((current) => (current === target ? null : current));
  }

  const subnetsOf = (name: string) => networks.find((n) => n.name === name)?.subnets ?? [];
  const known = (name: string) => networks.some((n) => n.name === name);
  // A row error is shown once by ListEditor; flag the control it most plausibly belongs to.
  function nameInvalid(item: NetworkAttachment, i: number) {
    const msg = errors[i];
    return !!msg && (!item.name || (/network/i.test(msg) && !/\bip/i.test(msg)));
  }
  const ipInvalid = (item: NetworkAttachment, i: number) => !!errors[i] && !!item.name && !nameInvalid(item, i);

  return (
    <>
      {failed && (
        <p className={styles.note} role="status">
          Couldn't list networks (is Docker running?). Type the network name instead.
        </p>
      )}
      <ListEditor<NetworkAttachment>
        title="Networks"
        items={value}
        onChange={onChange}
        readonly={readonly}
        errors={errors}
        addLabel="Add network"
        newItem={() => ({ name: '', ipv4Address: '' })}
        row={(item, i, set) => (
          <div className={styles.cols}>
            <Field label="Network" id={`${prefix}-${i}-name`} readonly={readonly}>
              {({ readOnly, ...c }) => {
                const invalid = nameInvalid(item, i) ? 'true' : c['aria-invalid'];
                return failed ? (
                  <input {...c} readOnly={readOnly} aria-invalid={invalid} type="text" value={item.name} onChange={(e) => set(rename(item, e.target.value))} />
                ) : (
                  <select
                    {...c}
                    aria-invalid={invalid}
                    disabled={readOnly}
                    value={pending?.index === i ? CREATE : item.name}
                    onChange={(e) => choose(e.target.value, item, i, set)}
                  >
                    <option value="">Select a network</option>
                    {item.name && !known(item.name) && <option value={item.name}>{item.name}</option>}
                    {networks.map((n) => (
                      <option key={n.name} value={n.name}>
                        {label(n)}
                      </option>
                    ))}
                    <option value={CREATE} disabled={readOnly}>
                      Create network…
                    </option>
                  </select>
                );
              }}
            </Field>
            {item.name && (
              <Field label="Static IPv4 (optional)" id={`${prefix}-${i}-ip`} readonly={readonly}>
                {(c) => (
                  <input
                    {...c}
                    aria-invalid={ipInvalid(item, i) ? 'true' : c['aria-invalid']}
                    type="text"
                    value={item.ipv4Address}
                    placeholder={subnetsOf(item.name)[0] ? `e.g. in ${subnetsOf(item.name)[0]}` : ''}
                    onChange={(e) => set({ ...item, ipv4Address: e.target.value })}
                  />
                )}
              </Field>
            )}
          </div>
        )}
      />
      <CreateNetworkDialog open={creating} onClose={closeCreate} onCreated={networkCreated} />
    </>
  );
}
