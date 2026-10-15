<script lang="ts">
  import { tick } from 'svelte';

  let {
    text,
    label = 'Copy',
    variant = 'ghost',
    size = 'xs',
  }: { text: string; label?: string; variant?: 'ghost' | 'icon'; size?: 'xs' | 'sm' } = $props();

  let copied = $state(false);
  let failed = $state(false);
  let button = $state<HTMLButtonElement>();
  let timer: ReturnType<typeof setTimeout> | undefined;

  function fallbackCopy(value: string): boolean {
    const area = document.createElement('textarea');
    area.value = value;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    try {
      return document.execCommand('copy');
    } finally {
      area.remove();
      button?.focus(); // select() moved focus to the textarea
    }
  }

  async function copy() {
    let ok = false;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
        ok = true;
      }
    } catch {
      ok = false;
    }
    if (!ok) ok = fallbackCopy(text);
    // Clear first so a repeat copy changes the live region and is announced again.
    copied = false;
    failed = false;
    await tick();
    if (!ok) {
      failed = true;
      return;
    }
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 2000);
  }

  $effect(() => () => clearTimeout(timer));
</script>

<button
  type="button"
  class="copy {variant} {size}"
  bind:this={button}
  aria-label={variant === 'icon' ? label : undefined}
  title={variant === 'icon' ? label : undefined}
  onclick={copy}>{#if variant === 'icon'}<span aria-hidden="true">⧉</span>{:else}{label}{/if}</button
>
<span role="status" class={failed ? 'bad' : 'visually-hidden'}
  >{copied ? 'Copied' : failed ? 'Copy failed — select and copy manually' : ''}</span
>
{#if copied}<span class="ok" aria-hidden="true">Copied</span>{/if}

<style>
  .copy {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 24px;
    padding: 0 8px;
    background: transparent;
    color: var(--muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .copy.sm { height: 26px; padding: 0 10px; }
  .copy.icon { width: 24px; padding: 0; }
  .copy.icon.sm { width: 26px; }
  .copy:hover { background: var(--hover); color: var(--text); }
  .bad { color: var(--danger); margin-inline-start: var(--space-2); }
  .ok { color: var(--success); margin-inline-start: var(--space-2); }
</style>
