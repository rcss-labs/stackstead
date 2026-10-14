<script lang="ts">
  import { toasts } from '../stores/toasts';

  const icon = { info: 'ℹ', success: '✔', warning: '⚠', danger: '✖' } as const;
</script>

<section class="toasts" aria-label="Notifications" aria-live="polite">
  {#each $toasts as toast (toast.id)}
    <div class="toast {toast.kind}">
      <span class="icon" aria-hidden="true">{icon[toast.kind]}</span>
      <span class="message">{toast.message}</span>
      <button type="button" aria-label="Dismiss notification" onclick={() => toasts.dismiss(toast.id)}>×</button>
    </div>
  {/each}
</section>

<style>
  .toasts {
    position: fixed;
    inset-block-end: var(--space-4);
    inset-inline: var(--space-4);
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: var(--space-2);
    pointer-events: none;
    z-index: 100;
  }
  .toast {
    pointer-events: auto;
    display: flex;
    gap: var(--space-3);
    align-items: center;
    max-width: min(28rem, 100%);
    padding: var(--space-3) var(--space-4);
    background: var(--surface-raised);
    border: 1px solid var(--tone);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgb(0 0 0 / 0.2);
  }
  .info { --tone: var(--info); }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .icon { color: var(--tone); }
  .message { overflow-wrap: anywhere; }
  button {
    background: none;
    border: 0;
    cursor: pointer;
    font-size: 1.25rem;
    line-height: 1;
    padding: var(--space-1);
  }
</style>
