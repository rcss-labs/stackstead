<script module lang="ts">
  // Open dialogs, oldest first. Escape closes only the last one.
  const openStack: symbol[] = [];
  // Buttons clicked with a mouse do not take focus in Safari or Firefox on macOS, so the last
  // click target stands in as the opener when nothing is focused.
  let lastClick: { el: HTMLElement; at: number } | undefined;
  if (typeof document !== 'undefined') {
    document.addEventListener('click', (event) => {
      const button = event.target instanceof Element ? event.target.closest<HTMLElement>('button, a[href], [role="button"]') : null;
      lastClick = button ? { el: button, at: Date.now() } : undefined;
    }, true);
  }
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
    const active = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;
    const opener = active ?? (lastClick && Date.now() - lastClick.at < 1000 && lastClick.el.isConnected ? lastClick.el : null);
    const appRoot = document.getElementById('app');
    const wasInert = appRoot?.inert ?? false;
    if (appRoot) appRoot.inert = true;
    (focusables()[0] ?? dialogEl).focus();
    dialogEl.scrollTop = 0;
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
    background: rgba(0, 0, 0, 0.5);
    z-index: 200;
  }
  .dialog {
    width: min(32rem, 100%);
    max-height: 100%;
    overflow: auto;
    padding: 20px;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-pop);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .dialog :global(h2) { font-size: 14px; font-weight: 600; }
</style>
