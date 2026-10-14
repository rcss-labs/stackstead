<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../api/client';
  import type { Network } from '../api/types';
  import Field from '../components/Field.svelte';
  import ListEditor from '../components/ListEditor.svelte';

  type Attachment = { name: string; ipv4Address: string };

  let {
    value,
    onchange,
    readonly = false,
    errors = {},
    idPrefix,
  }: {
    value: Attachment[];
    onchange: (v: Attachment[]) => void;
    readonly?: boolean;
    errors?: Record<number, string>;
    /** Stable id prefix: controls get `${idPrefix}-{row}-name` and `-ip`. */
    idPrefix?: string;
  } = $props();

  const uid = $props.id();
  const prefix = $derived(idPrefix ?? uid);
  let networks = $state<Network[]>([]);
  let failed = $state(false);

  onMount(() => {
    let alive = true;
    api.networks().then(
      (list) => {
        if (alive) networks = list;
      },
      () => {
        if (alive) failed = true;
      },
    );
    return () => {
      alive = false;
    };
  });

  const subnetsOf = (name: string) => networks.find((n) => n.name === name)?.subnets ?? [];
  const label = (n: Network) => (n.subnets.length ? `${n.name} (${n.subnets.join(', ')})` : n.name);
  // A row error is shown once by ListEditor; flag the control it most plausibly belongs to.
  function nameInvalid(item: Attachment, i: number) {
    const msg = errors[i];
    return !!msg && (!item.name || (/network/i.test(msg) && !/\bip/i.test(msg)));
  }
  const ipInvalid = (item: Attachment, i: number) => !!errors[i] && !!item.name && !nameInvalid(item, i);
  const rename = (item: Attachment, name: string): Attachment => ({ name, ipv4Address: name ? item.ipv4Address : '' });
  const known = (name: string) => networks.some((n) => n.name === name);
</script>

{#if failed}
  <p class="note" role="status">
    Couldn't list networks (is Docker running?). Type the network name instead.
  </p>
{/if}

<ListEditor items={value} {onchange} {readonly} {errors} addLabel="Add network" newItem={() => ({ name: '', ipv4Address: '' })}>
  {#snippet row(item: Attachment, i: number, set: (v: Attachment) => void)}
    <div class="cols">
      {#if failed}
        <Field label="Network" id="{prefix}-{i}-name" {readonly}>
          {#snippet children(c)}
            <input {...c} aria-invalid={nameInvalid(item, i) ? 'true' : c['aria-invalid']} type="text" value={item.name} oninput={(e) => set(rename(item, e.currentTarget.value))} />
          {/snippet}
        </Field>
      {:else}
        <Field label="Network" id="{prefix}-{i}-name" {readonly}>
          {#snippet children({ readonly: ro, ...c })}
            <select {...c} aria-invalid={nameInvalid(item, i) ? 'true' : c['aria-invalid']} disabled={ro} value={item.name} onchange={(e) => set(rename(item, e.currentTarget.value))}>
              <option value="">Select a network</option>
              {#if item.name && !known(item.name)}<option value={item.name}>{item.name}</option>{/if}
              {#each networks as n (n.name)}
                <option value={n.name}>{label(n)}</option>
              {/each}
            </select>
          {/snippet}
        </Field>
      {/if}
      {#if item.name}
        <Field label="Static IPv4 (optional)" id="{prefix}-{i}-ip" {readonly}>
          {#snippet children(c)}
            <input
              {...c}
              aria-invalid={ipInvalid(item, i) ? 'true' : c['aria-invalid']}
              type="text"
              value={item.ipv4Address}
              placeholder={subnetsOf(item.name)[0] ? `e.g. in ${subnetsOf(item.name)[0]}` : ''}
              oninput={(e) => set({ ...item, ipv4Address: e.currentTarget.value })}
            />
          {/snippet}
        </Field>
      {/if}
    </div>
  {/snippet}
</ListEditor>

<style>
  .cols { display: flex; flex-wrap: wrap; gap: var(--space-3); flex: 1; }
  .cols > :global(*) { flex: 1 1 12rem; }
  .note { color: var(--muted); font-size: 0.875rem; }
</style>
