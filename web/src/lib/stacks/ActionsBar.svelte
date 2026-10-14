<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { ACTIONS, type StackSummary } from '$lib/api/types';

  let {
    stack,
    lifecycleDisabledReason,
    busy = false,
    onact,
    onremove,
  }: {
    stack: StackSummary;
    lifecycleDisabledReason?: string;
    busy?: boolean;
    onact: (action: (typeof ACTIONS)[number]) => void;
    onremove: (withVolumes: boolean) => void;
  } = $props();

  const uid = $props.id();
  let confirmOpen = $state(false);
  let withVolumes = $state(false);

  const reason = $derived(lifecycleDisabledReason || (busy ? 'an operation is already running' : undefined));
  const label = (a: string) => a.charAt(0).toUpperCase() + a.slice(1);

  function openConfirm() {
    withVolumes = false;
    confirmOpen = true;
  }
  function confirm() {
    confirmOpen = false;
    onremove(withVolumes);
  }
</script>

<div class="cluster" role="group" aria-label="Actions for {stack.name}">
  {#each ACTIONS as action (action)}
    <Button variant={action === 'up' ? 'primary' : 'secondary'} disabledReason={reason} onclick={() => onact(action)}>
      {label(action)}
    </Button>
  {/each}
  <Button variant="danger" onclick={openConfirm}>Remove</Button>
</div>

<ConfirmDialog
  open={confirmOpen}
  title="Remove {stack.name}?"
  message="This stops and removes the stack's containers and networks and unregisters it from Stackstead."
  confirmLabel="Remove"
  danger
  onconfirm={confirm}
  oncancel={() => (confirmOpen = false)}
>
  <label class="cluster" for="{uid}-volumes">
    <input id="{uid}-volumes" type="checkbox" bind:checked={withVolumes} />
    Also remove volumes
  </label>
  <p class="muted">Files on disk are never deleted.</p>
</ConfirmDialog>
