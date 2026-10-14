<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import { formatDuration } from '$lib/format';
  import type { Operation } from '$lib/api/types';
  import { createOperationStream } from './operation';

  let {
    operationId,
    oncancel,
    onfinished,
    autoscroll = true,
  }: {
    operationId: string;
    oncancel?: (id: string) => void;
    onfinished?: (op: Operation) => void;
    autoscroll?: boolean;
  } = $props();

  // The stream is bound to one operation id; a new id means a new panel (callers key on it).
  // svelte-ignore state_referenced_locally
  const stream = createOperationStream(operationId);
  const { lines, op, status } = stream;
  onDestroy(() => stream.close());

  let region: HTMLElement | undefined = $state();
  let following = $state(true);
  let now = $state(Date.now());
  let notified = false;

  const running = $derived(!$op || $op.state === 'running');
  // The first line is the "N earlier lines dropped" note when the server sent one.
  const hasNote = $derived(/^\.\.\. \d+ earlier lines dropped$/.test($lines[0] ?? ''));
  const elapsed = $derived.by(() => {
    if (!$op) return undefined;
    const start = Date.parse($op.startedAt);
    const end = $op.endedAt ? Date.parse($op.endedAt) : now;
    return Number.isNaN(start) || Number.isNaN(end) ? undefined : formatDuration(Math.max(0, end - start));
  });
  const announcement = $derived(
    $op && $op.state !== 'running'
      ? `${$op.action} ${$op.state}`
      : $status === 'reconnecting'
        ? 'Connection lost, reconnecting'
        : '',
  );

  $effect(() => {
    if (!running) return;
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    const finished = $op;
    if (finished && finished.state !== 'running' && !notified) {
      notified = true;
      onfinished?.(finished);
    }
  });

  $effect(() => {
    void $lines;
    if (autoscroll && following) void tick().then(scrollToEnd);
  });

  function scrollToEnd() {
    if (region) region.scrollTop = region.scrollHeight;
  }
  function onscroll() {
    if (!region) return;
    following = region.scrollHeight - region.scrollTop - region.clientHeight < 24;
  }
  function jump() {
    following = true;
    scrollToEnd();
  }
</script>

<section class="panel" aria-label="Operation output">
  <header class="cluster">
    {#if $op}
      <strong>{$op.action}</strong>
      <span>{$op.stack}</span>
      <StatusBadge status={$op.state} />
      {#if elapsed}<span class="muted">{elapsed}</span>{/if}
      {#if $op.exitCode !== undefined}<span class="muted">exit code {$op.exitCode}</span>{/if}
    {:else}
      <span class="muted">Connecting…</span>
    {/if}
    {#if running}
      <Button variant="secondary" onclick={() => oncancel?.(operationId)}>Cancel</Button>
    {/if}
  </header>
  {#if $op?.error}<p class="error">{$op.error}</p>{/if}
  <div class="visually-hidden" role="status" aria-live="polite">{announcement}</div>
  <div class="wrap">
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="log" role="log" aria-live="off" aria-label="Output" tabindex="0" bind:this={region} {onscroll}>
      {#each $lines as text, i (i)}
        <div class={['line', hasNote && i === 0 && 'note']}>{text}</div>
      {/each}
    </div>
    {#if !following}
      <button type="button" class="jump" onclick={jump}>Jump to latest</button>
    {/if}
  </div>
</section>

<style>
  .panel { display: grid; gap: var(--space-2); min-width: 0; }
  .wrap { position: relative; }
  .log {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.8125rem;
    max-height: 24rem;
    overflow: auto;
    padding: var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--surface-raised);
  }
  .line { white-space: pre-wrap; overflow-wrap: anywhere; }
  .note { opacity: 0.6; font-style: italic; }
  .error { color: var(--danger); overflow-wrap: anywhere; margin: 0; }
  .jump { position: absolute; right: var(--space-3); bottom: var(--space-3); }
</style>
