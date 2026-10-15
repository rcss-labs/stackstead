<script lang="ts">
  import { toasts } from '../stores/toasts';

  // Render under <body>, not #app: a modal Dialog makes #app inert, and toasts must stay reachable.
  function portal(node: HTMLElement) {
    document.body.appendChild(node);
    return { destroy: () => node.remove() };
  }
</script>

<section class="toasts" aria-label="Notifications" aria-live="polite" use:portal>
  {#each $toasts as toast (toast.id)}
    <div class="toast {toast.kind}" role={toast.kind === 'danger' ? 'alert' : undefined}>
      <span class="dot" aria-hidden="true"></span>
      <span class="message">{toast.message}</span>
      <button type="button" aria-label="Dismiss notification" onclick={() => toasts.dismiss(toast.id)}>×</button>
    </div>
  {/each}
</section>

<style>
  .toasts {
    position: fixed;
    inset-block-end: 16px;
    inset-inline: 16px;
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
    padding: 10px 14px;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-pop);
    font-size: 12.5px;
  }
  .info { --tone: var(--info); }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .dot { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--tone); }
  .message { overflow-wrap: anywhere; }
  button {
    background: none;
    color: var(--muted);
    border: 0;
    cursor: pointer;
    font-size: 1.25rem;
    line-height: 1;
    padding: var(--space-1);
  }
</style>
