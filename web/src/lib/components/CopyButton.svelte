<script lang="ts">
  import { tick } from 'svelte';

  let { text, label = 'Copy' }: { text: string; label?: string } = $props();

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

<button type="button" class="copy" bind:this={button} onclick={copy}>{label}</button>
<span role="status" class={failed ? 'bad' : 'visually-hidden'}
  >{copied ? 'Copied' : failed ? 'Copy failed — select and copy manually' : ''}</span
>
{#if copied}<span class="ok" aria-hidden="true">Copied</span>{/if}

<style>
  .copy {
    padding: var(--space-1) var(--space-3);
    background: var(--raised);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
  }
  .bad { color: var(--danger); margin-inline-start: var(--space-2); }
  .ok { color: var(--success); margin-inline-start: var(--space-2); }
</style>
