<script module lang="ts">
  // Open dialogs, oldest first. Escape closes only the last one.
  const openStack: symbol[] = [];
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    open,
    title,
    onclose,
    children,
    labelledBy,
    describedBy,
    role = 'dialog',
  }: {
    open: boolean;
    title: string;
    onclose: () => void;
    children?: Snippet;
    labelledBy?: string;
    describedBy?: string;
    role?: 'dialog' | 'alertdialog';
  } = $props();

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
    if (!dialogEl) return [];
    return Array.from(dialogEl.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
      (el) => !el.closest('[hidden], [inert]') && getComputedStyle(el).display !== 'none' && getComputedStyle(el).visibility !== 'hidden',
    );
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

  // Escape is handled on the document so it works wherever focus happens to be.
  $effect(() => {
    if (!open) return;
    const me = Symbol('dialog');
    openStack.push(me);
    const onEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      if (openStack[openStack.length - 1] !== me) return; // only the topmost dialog reacts
      event.preventDefault();
      onclose();
    };
    document.addEventListener('keydown', onEscape);
    return () => {
      document.removeEventListener('keydown', onEscape);
      openStack.splice(openStack.indexOf(me), 1);
    };
  });

  function onkeydown(event: KeyboardEvent) {
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
      {role}
      aria-modal="true"
      aria-labelledby={titleId}
      aria-describedby={describedBy}
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
