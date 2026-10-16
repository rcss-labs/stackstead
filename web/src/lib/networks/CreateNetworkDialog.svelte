<script lang="ts">
  import { untrack } from 'svelte';
  import { api, ApiError } from '../api/client';
  import type { Network, NetworkDriver, HostInterface, CreateNetworkRequest } from '../api/types';
  import Dialog from '../components/Dialog.svelte';
  import Field from '../components/Field.svelte';
  import Button from '../components/Button.svelte';
  import Banner from '../components/Banner.svelte';
  import { autofill, needsParent } from './networks';

  let { open, onclose, oncreated }: { open: boolean; onclose: () => void; oncreated: (network: Network) => void } = $props();
  let name = $state(''), driver = $state<NetworkDriver>('bridge');
  let subnet = $state(''), gateway = $state(''), ipRange = $state(''), parent = $state('');
  let ipvlanMode = $state<'l2' | 'l3'>('l2'), internal = $state(false);
  let interfaces = $state<HostInterface[]>([]), selected = $state('');
  let errors = $state<Record<string, string>>({}), banner = $state(''), busy = $state(false);
  let lastAuto = { subnet: '', gateway: '' };
  const lan = $derived(needsParent(driver));

  function applyInterface() {
    const iface = interfaces.find(i => i.name === selected);
    if (!iface) return;
    const filled = autofill(iface, { subnet, gateway }, lastAuto);
    subnet = filled.subnet; gateway = filled.gateway;
    lastAuto = { subnet: iface.subnet, gateway: iface.gateway };
    parent = iface.name;
  }
  function changeDriver(next: NetworkDriver) {
    driver = next;
    if (!needsParent(next)) parent = '';
    else applyInterface();
    if (next !== 'ipvlan') ipvlanMode = 'l2';
  }
  $effect(() => {
    if (!open) return;
    let alive = true;
    untrack(() => {
      name = ''; driver = 'bridge'; subnet = ''; gateway = ''; ipRange = ''; parent = '';
      ipvlanMode = 'l2'; internal = false; interfaces = []; selected = ''; errors = {}; banner = ''; busy = false;
      lastAuto = { subnet: '', gateway: '' };
      api.hostInterfaces().then(list => {
        if (!alive) return;
        interfaces = list;
        selected = (list.find(i => i.default) ?? list[0])?.name ?? '';
        if (needsParent(driver)) applyInterface();
      }, () => { if (alive) interfaces = []; });
    });
    return () => { alive = false; };
  });
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy) return;
    busy = true; errors = {}; banner = '';
    const body: CreateNetworkRequest = { name, driver };
    if (subnet) body.subnet = subnet;
    if (gateway) body.gateway = gateway;
    if (ipRange) body.ipRange = ipRange;
    if (lan && parent) body.parent = parent;
    if (driver === 'ipvlan') body.ipvlanMode = ipvlanMode;
    if (internal) body.internal = true;
    try {
      const network = await api.createNetwork(body);
      oncreated(network); onclose();
    } catch (error) {
      const known = ['name', 'driver', 'parent', 'subnet', 'gateway', 'ipRange', 'ipvlanMode', 'internal'];
      if (error instanceof ApiError && error.fields.length) {
        for (const field of error.fields) {
          if (known.includes(field.field)) errors[field.field] = field.message;
          else banner = error.message;
        }
      } else banner = error instanceof Error ? error.message : 'Could not create network';
    } finally { busy = false; }
  }
</script>

<Dialog {open} title="Create network" {onclose}>
  <form onsubmit={submit}>
    {#if banner}<Banner kind="danger" title={banner} />{/if}
    <Field label="Name" id="net-name" error={errors.name}>
      {#snippet children(c)}<input {...c} bind:value={name} required />{/snippet}
    </Field>
    <fieldset id="net-driver" role="radiogroup" aria-label="Driver" aria-describedby={errors.driver ? 'net-driver-error' : undefined}>
      <legend>Driver</legend>
      <div class="drivers">
        {#each [['bridge', 'Bridge'], ['macvlan', 'Macvlan'], ['ipvlan', 'IPvlan']] as [value, label]}
          <label><input type="radio" name="net-driver" {value} checked={driver === value} onchange={() => changeDriver(value as NetworkDriver)} />{label}</label>
        {/each}
      </div>
      {#if errors.driver}<p class="error" id="net-driver-error">{errors.driver}</p>{/if}
    </fieldset>
    {#if lan}
      {#if interfaces.length}
        <Field label="Host interface" id="net-parent" error={errors.parent}>
          {#snippet children({ readonly: _, ...c })}
            <select {...c} bind:value={selected} onchange={() => { if (selected === '__other__') parent = ''; else applyInterface(); }}>
              {#each interfaces as iface}<option value={iface.name}>{iface.name} — {iface.ipv4}{iface.default ? ' (default)' : ''}</option>{/each}
              <option value="__other__">Other…</option>
            </select>
          {/snippet}
        </Field>
      {/if}
      {#if !interfaces.length || selected === '__other__'}
        <Field label="Parent interface" id={interfaces.length ? 'net-parent-other' : 'net-parent'} error={errors.parent}>
          {#snippet children(c)}<input {...c} bind:value={parent} />{/snippet}
        </Field>
      {/if}
      <p role="note" class="note">The Docker host itself cannot reach containers on this network. Pick addresses outside your router's DHCP pool, or set an IP range outside it.</p>
    {/if}
    <Field label="Subnet" id="net-subnet" error={errors.subnet} mono hint={lan ? 'Required for macvlan and ipvlan' : undefined}>
      {#snippet children(c)}<input {...c} bind:value={subnet} />{/snippet}
    </Field>
    <Field label="Gateway" id="net-gateway" error={errors.gateway} mono>
      {#snippet children(c)}<input {...c} bind:value={gateway} />{/snippet}
    </Field>
    <Field label="IP range (optional)" id="net-iprange" error={errors.ipRange} mono>
      {#snippet children(c)}<input {...c} bind:value={ipRange} />{/snippet}
    </Field>
    {#if driver === 'ipvlan'}
      <Field label="IPvlan mode" id="net-ipvlan-mode" error={errors.ipvlanMode}>
        {#snippet children({ readonly: _, ...c })}<select {...c} bind:value={ipvlanMode}><option value="l2">l2</option><option value="l3">l3</option></select>{/snippet}
      </Field>
    {/if}
    <details><summary>Advanced</summary>
      <Field label="Internal" id="net-internal" error={errors.internal}>
        {#snippet children({ readonly: _, ...c })}<input {...c} type="checkbox" bind:checked={internal} />{/snippet}
      </Field>
    </details>
    <div class="actions"><Button variant="secondary" onclick={onclose}>Cancel</Button><Button variant="primary" type="submit" loading={busy}>Create network</Button></div>
  </form>
</Dialog>
<style>
  form { display: flex; flex-direction: column; gap: var(--space-3); min-width: 0; }
  fieldset { border: 0; padding: 0; min-width: 0; }
  legend, summary { color: var(--muted); font-size: 12px; }
  .drivers { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-1); }
  .drivers label { display: flex; align-items: center; gap: var(--space-1); padding: var(--space-2); border: 1px solid var(--border); border-radius: var(--radius); }
  .drivers label:has(:checked) { border-color: var(--accent); background: var(--raised); }
  .drivers label:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
  .note { font-size: 12px; color: var(--muted); overflow-wrap: anywhere; }
  .error { font-size: 12px; color: var(--danger); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
  summary { cursor: pointer; margin-bottom: var(--space-2); }
</style>
