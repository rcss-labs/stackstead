<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    open,
    title,
    onclose,
    children,
    labelledBy,
  }: { open: boolean; title: string; onclose: () => void; children?: Snippet; labelledBy?: string } = $props();

  const uid = $props.id();
  const titleId = $derived(labelledBy ?? `${uid}-title`);
  const FOCUSABLE =
    'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  let dialogEl = $state<HTMLElement>();

  // Render outside the app root so that root can be made inert.
  function portal(node: HTMLElement) {
    document.body.appendChild(node);
    return { destroy: () => node.remove() };
  }

  function focusables(): HTMLElement[] {
    return dialogEl ? Array.from(dialogEl.querySelectorAll<HTMLElement>(FOCUSABLE)) : [];
  }

  $effect(() => {
    if (!open || !dialogEl) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const appRoot = document.getElementById('app');
    const wasInert = appRoot?.inert ?? false;
    if (appRoot) appRoot.inert = true;
    (focusables()[0] ?? dialogEl).focus();
    return () => {
      if (appRoot) appRoot.inert = wasInert;
      opener?.focus();
    };
  });

  function onkeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      onclose();
      return;
    }
    if (event.key !== 'Tab') return;
    const items = focusables();
    if (items.length === 0) {
      event.preventDefault();
      dialogEl?.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === dialogEl)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    } else if (!dialogEl?.contains(active)) {
      event.preventDefault();
      first.focus();
    }
  }
</script>

{#if open}
  <div class="backdrop" use:portal>
    <div
      bind:this={dialogEl}
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      tabindex="-1"
      {onkeydown}
    >
      {#if !labelledBy}<h2 id={titleId}>{title}</h2>{/if}
      {@render children?.()}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    display: grid;
    place-items: center;
    padding: var(--space-4);
    background: rgb(0 0 0 / 0.5);
    z-index: 200;
  }
  .dialog {
    width: min(32rem, 100%);
    max-height: 100%;
    overflow: auto;
    padding: var(--space-5);
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
</style>
