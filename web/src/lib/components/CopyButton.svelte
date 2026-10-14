<script lang="ts">
  let { text, label = 'Copy' }: { text: string; label?: string } = $props();

  let copied = $state(false);
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
    if (!ok) return;
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 2000);
  }

  $effect(() => () => clearTimeout(timer));
</script>

<button type="button" class="copy" onclick={copy}>{label}</button>
<span role="status" class="visually-hidden">{copied ? 'Copied' : ''}</span>
{#if copied}<span class="ok" aria-hidden="true">Copied</span>{/if}

<style>
  .copy {
    padding: var(--space-1) var(--space-3);
    background: var(--surface-raised);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
  }
  .ok { color: var(--success); margin-inline-start: var(--space-2); }
</style>
