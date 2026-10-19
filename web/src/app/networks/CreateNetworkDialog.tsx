import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { CreateNetworkRequest, HostInterface, Network, NetworkDriver } from '$lib/api/types';
import { autofill, needsParent } from '$lib/networks/networks';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import styles from './CreateNetworkDialog.module.css';

export interface CreateNetworkDialogProps {
  open: boolean;
  onClose: () => void;
  onCreated: (network: Network) => void;
}

interface Form {
  name: string;
  driver: NetworkDriver;
  subnet: string;
  gateway: string;
  ipRange: string;
  parent: string;
  ipvlanMode: 'l2' | 'l3';
  internal: boolean;
  interfaces: HostInterface[];
  selected: string;
}

const emptyForm: Form = {
  name: '',
  driver: 'bridge',
  subnet: '',
  gateway: '',
  ipRange: '',
  parent: '',
  ipvlanMode: 'l2',
  internal: false,
  interfaces: [],
  selected: '',
};

const DRIVERS: [NetworkDriver, string][] = [
  ['bridge', 'Bridge'],
  ['macvlan', 'Macvlan'],
  ['ipvlan', 'IPvlan'],
];
const KNOWN_FIELDS = ['name', 'driver', 'parent', 'subnet', 'gateway', 'ipRange', 'ipvlanMode', 'internal'];
const DHCP_NOTE = "The Docker host itself cannot reach containers on this network. Pick addresses outside your router's DHCP pool, or set an IP range outside it.";

export function CreateNetworkDialog({ open, onClose, onCreated }: CreateNetworkDialogProps) {
  const [form, setForm] = useState<Form>(emptyForm);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [banner, setBanner] = useState('');
  const [busy, setBusy] = useState(false);
  // Event handlers and async completions read the latest values through the ref, not a stale render.
  const formRef = useRef(form);
  const lastAuto = useRef({ subnet: '', gateway: '' });
  // A new token per opening (and none once cancelled): completions of an older request are dropped.
  const session = useRef<object | undefined>(undefined);
  const busyRef = useRef(false);

  function update(patch: Partial<Form>) {
    formRef.current = { ...formRef.current, ...patch };
    setForm(formRef.current);
  }

  function cancel() {
    session.current = undefined;
    onClose();
  }

  // Fill subnet, gateway and parent from the selected host interface, keeping manual edits.
  function applyInterface(selected = formRef.current.selected): Partial<Form> {
    const current = formRef.current;
    const iface = current.interfaces.find((i) => i.name === selected);
    if (!iface) return {};
    const filled = autofill(iface, current, lastAuto.current);
    lastAuto.current = { subnet: iface.subnet, gateway: iface.gateway };
    return { ...filled, parent: iface.name };
  }

  function changeDriver(driver: NetworkDriver) {
    update({
      driver,
      ...(needsParent(driver) ? applyInterface() : { parent: '' }),
      ...(driver !== 'ipvlan' ? { ipvlanMode: 'l2' as const } : {}),
    });
  }

  function changeInterface(selected: string) {
    update({ selected, ...(selected === '__other__' ? { parent: '' } : applyInterface(selected)) });
  }

  useEffect(() => {
    if (!open) return;
    let alive = true;
    const opened = {};
    session.current = opened;
    busyRef.current = false;
    lastAuto.current = { subnet: '', gateway: '' };
    formRef.current = emptyForm;
    setForm(emptyForm);
    setErrors({});
    setBanner('');
    setBusy(false);
    api.hostInterfaces().then(
      (list) => {
        if (!alive) return;
        update({ interfaces: list, selected: (list.find((i) => i.default) ?? list[0])?.name ?? '' });
        if (needsParent(formRef.current.driver)) update(applyInterface());
      },
      () => {
        if (alive) update({ interfaces: [] });
      },
    );
    return () => {
      alive = false;
      if (session.current === opened) session.current = undefined;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only a (re)opening starts a session
  }, [open]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busyRef.current || !session.current) return;
    const submitted = session.current;
    const created = onCreated;
    const close = onClose;
    const f = formRef.current;
    busyRef.current = true;
    setBusy(true);
    setErrors({});
    setBanner('');
    const body: CreateNetworkRequest = { name: f.name, driver: f.driver };
    if (f.subnet) body.subnet = f.subnet;
    if (f.gateway) body.gateway = f.gateway;
    if (f.ipRange) body.ipRange = f.ipRange;
    if (needsParent(f.driver) && f.parent) body.parent = f.parent;
    if (f.driver === 'ipvlan') body.ipvlanMode = f.ipvlanMode;
    if (f.internal) body.internal = true;
    try {
      const network = await api.createNetwork(body);
      if (session.current !== submitted) return;
      created(network);
      close();
    } catch (error) {
      if (session.current !== submitted) return;
      if (error instanceof ApiError && error.fields.length) {
        const mapped: Record<string, string> = {};
        let other = '';
        for (const field of error.fields) {
          if (KNOWN_FIELDS.includes(field.field)) mapped[field.field] = field.message;
          else other = error.message;
        }
        setErrors(mapped);
        setBanner(other);
      } else {
        setBanner(error instanceof Error ? error.message : 'Could not create network');
      }
    } finally {
      if (session.current === submitted) {
        busyRef.current = false;
        setBusy(false);
      }
    }
  }

  const lan = needsParent(form.driver);
  const hasInterfaces = form.interfaces.length > 0;

  return (
    <Dialog open={open} title="Create network" onClose={cancel}>
      <form className={styles.form} onSubmit={submit}>
        {banner && <Banner kind="danger" title={banner} />}
        <Field label="Name" id="net-name" error={errors.name}>
          {({ readOnly: _, ...c }) => <input {...c} value={form.name} required onChange={(e) => update({ name: e.target.value })} />}
        </Field>
        <fieldset id="net-driver" role="radiogroup" aria-label="Driver" aria-describedby={errors.driver ? 'net-driver-error' : undefined} className={styles.drivers}>
          <legend>Driver</legend>
          <div className={styles.options}>
            {DRIVERS.map(([value, label]) => (
              <label key={value}>
                <input type="radio" name="net-driver" value={value} checked={form.driver === value} onChange={() => changeDriver(value)} />
                {label}
              </label>
            ))}
          </div>
          {errors.driver && (
            <p className={styles.error} id="net-driver-error">
              {errors.driver}
            </p>
          )}
        </fieldset>
        {lan && (
          <>
            {hasInterfaces && (
              <Field label="Host interface" id="net-parent" error={errors.parent}>
                {({ readOnly: _, ...c }) => (
                  <select {...c} value={form.selected} onChange={(e) => changeInterface(e.target.value)}>
                    {form.interfaces.map((iface) => (
                      <option key={iface.name} value={iface.name}>
                        {iface.name} — {iface.ipv4}
                        {iface.default ? ' (default)' : ''}
                      </option>
                    ))}
                    <option value="__other__">Other…</option>
                  </select>
                )}
              </Field>
            )}
            {(!hasInterfaces || form.selected === '__other__') && (
              <Field label="Parent interface" id={hasInterfaces ? 'net-parent-other' : 'net-parent'} error={errors.parent}>
                {({ readOnly: _, ...c }) => <input {...c} value={form.parent} onChange={(e) => update({ parent: e.target.value })} />}
              </Field>
            )}
            <p role="note" className={styles.note}>
              {DHCP_NOTE}
            </p>
          </>
        )}
        <Field label="Subnet" id="net-subnet" error={errors.subnet} mono hint={lan ? 'Required for macvlan and ipvlan' : undefined}>
          {({ readOnly: _, ...c }) => <input {...c} value={form.subnet} onChange={(e) => update({ subnet: e.target.value })} />}
        </Field>
        <Field label="Gateway" id="net-gateway" error={errors.gateway} mono>
          {({ readOnly: _, ...c }) => <input {...c} value={form.gateway} onChange={(e) => update({ gateway: e.target.value })} />}
        </Field>
        <Field label="IP range (optional)" id="net-iprange" error={errors.ipRange} mono>
          {({ readOnly: _, ...c }) => <input {...c} value={form.ipRange} onChange={(e) => update({ ipRange: e.target.value })} />}
        </Field>
        {form.driver === 'ipvlan' && (
          <Field label="IPvlan mode" id="net-ipvlan-mode" error={errors.ipvlanMode}>
            {({ readOnly: _, ...c }) => (
              <select {...c} value={form.ipvlanMode} onChange={(e) => update({ ipvlanMode: e.target.value as 'l2' | 'l3' })}>
                <option value="l2">l2</option>
                <option value="l3">l3</option>
              </select>
            )}
          </Field>
        )}
        <details className={styles.advanced}>
          <summary>Advanced</summary>
          <Field label="Internal" id="net-internal" error={errors.internal}>
            {({ readOnly: _, ...c }) => <input {...c} type="checkbox" checked={form.internal} onChange={(e) => update({ internal: e.target.checked })} />}
          </Field>
        </details>
        <div className={styles.actions}>
          <Button variant="secondary" onClick={cancel}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={busy}>
            Create network
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
