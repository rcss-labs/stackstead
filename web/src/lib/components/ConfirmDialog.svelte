<script lang="ts">
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';

  let {
    open,
    title,
    message,
    confirmLabel,
    danger = false,
    onconfirm,
    oncancel,
  }: {
    open: boolean;
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();
</script>

<Dialog {open} {title} onclose={oncancel}>
  <p>{message}</p>
  <div class="cluster actions">
    <!-- Cancel comes first so initial focus never lands on the destructive action. -->
    <Button variant="secondary" onclick={oncancel}>Cancel</Button>
    <Button variant={danger ? 'danger' : 'primary'} onclick={onconfirm}>{confirmLabel}</Button>
  </div>
</Dialog>

<style>
  .actions { justify-content: flex-end; }
  p { overflow-wrap: anywhere; }
</style>
