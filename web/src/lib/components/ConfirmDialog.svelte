<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';

  let {
    open,
    title,
    message,
    confirmLabel,
    danger = false,
    confirmDisabledReason,
    pending = false,
    onconfirm,
    oncancel,
    children,
  }: {
    open: boolean;
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    confirmDisabledReason?: string;
    pending?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
    /** Extra content between the message and the actions (e.g. an option checkbox). */
    children?: Snippet;
  } = $props();

  const uid = $props.id();
</script>

<Dialog {open} {title} onclose={oncancel} role="alertdialog" describedBy="{uid}-message">
  <p id="{uid}-message">{message}</p>
  {@render children?.()}
  <div class="cluster actions">
    <!-- Cancel comes first so initial focus never lands on the destructive action. -->
    <Button variant="secondary" onclick={oncancel}>Cancel</Button>
    <Button variant={danger ? 'danger' : 'primary'} confirm={danger} disabledReason={confirmDisabledReason} loading={pending} onclick={onconfirm}>{confirmLabel}</Button>
  </div>
</Dialog>

<style>
  .actions { justify-content: flex-end; }
  p { overflow-wrap: anywhere; }
</style>
