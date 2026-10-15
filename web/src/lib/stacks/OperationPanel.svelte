<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import { formatDuration } from '$lib/format';
  import type { Operation } from '$lib/api/types';
  import { createOperationStream, operationDurationMs, operationStateLook, outputTone } from './operation';

  let {
    operationId,
    oncancel,
    onfinished,
    autoscroll = true,
  }: {
    operationId: string;
    oncancel?: (id: string) => void | Promise<void>;
    onfinished?: (op: Operation) => void;
    autoscroll?: boolean;
  } = $props();

  // The stream is bound to one operation id; a new id means a new panel (callers key on it).
  // svelte-ignore state_referenced_locally
  const stream = createOperationStream(operationId);
  const { lines, op, status, unavailable } = stream;
  onDestroy(() => stream.close());

  let region: HTMLElement | undefined = $state();
  let following = $state(true);
  let now = $state(Date.now());
  let notified = false;
  let cancelling = $state(false);

  const running = $derived(!$unavailable && (!$op || $op.state === 'running'));
  const elapsed = $derived.by(() => {
    if (!$op) return undefined;
    const ms = operationDurationMs($op, now);
    return ms === undefined ? undefined : formatDuration(ms);
  });
  const look = $derived($op ? operationStateLook($op.state) : undefined);
  const outputText = $derived($lines.map((entry) => entry.text).join('\n'));
  const announcement = $derived(
    $unavailable
      ? 'Operation no longer available'
      : $op && $op.state !== 'running'
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

  async function cancel() {
    if (cancelling || !oncancel) return;
    cancelling = true;
    const result = oncancel(operationId);
    // A synchronous handler stays "cancelling" until the operation ends and the button goes away.
    if (result instanceof Promise) {
      try {
        await result;
      } catch {
        /* the caller reports its own errors */
      } finally {
        cancelling = false;
      }
    }
  }

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
  <header>
    {#if $op && look}
      <strong class="cmd">docker compose {$op.action}</strong>
      <span class="state {look.tone}">{look.label}</span>
      <span class="meta">{elapsed ?? ''} · exit {$op.exitCode ?? '—'}</span>
    {:else if $unavailable}
      <span class="muted">Operation no longer available</span>
    {:else}
      <span class="muted">Connecting…</span>
    {/if}
    <span class="spacer"></span>
    {#if running}
      <Button variant="secondary" loading={cancelling} onclick={cancel}>Cancel</Button>
    {/if}
    <CopyButton text={outputText} label="Copy output" size="xs" />
  </header>
  {#if $op?.error}<p class="error">{$op.error}</p>{/if}
  <div class="visually-hidden" role="status" aria-live="polite">{announcement}</div>
  <div class="wrap">
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="log" role="log" aria-live="off" aria-label="Output" tabindex="0" bind:this={region} {onscroll}>
      {#each $lines as entry (entry.key)}
        <div class={['line', entry.kind !== 'line' ? entry.kind : outputTone(entry.text)]}>{entry.text}</div>
      {/each}
    </div>
    {#if !following}
      <button type="button" class="jump" onclick={jump}>Jump to latest</button>
    {/if}
  </div>
</section>

<style>
  .panel { display: grid; gap: var(--space-2); min-width: 0; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-2); }
  header { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); font-size: 12px; }
  .cmd { font-family: var(--mono); font-weight: 500; }
  .state { font-weight: 600; font-size: 11px; text-transform: uppercase; letter-spacing: 0.02em; color: var(--tone); }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .muted { --tone: var(--muted); color: var(--muted); }
  .meta { color: var(--muted); font-family: var(--mono); }
  .spacer { flex: 1; }
  .wrap { position: relative; }
  .log {
    font-family: var(--mono);
    font-size: 11.5px;
    line-height: 18px;
    max-height: 260px;
    overflow: auto;
    padding: var(--space-2);
    border-radius: var(--radius);
    background: var(--bg);
  }
  .line { white-space: pre-wrap; overflow-wrap: anywhere; }
  .line.success { color: var(--success); }
  .line.danger { color: var(--danger); }
  .note, .gap { color: var(--muted); font-style: italic; }
  .error { color: var(--danger); overflow-wrap: anywhere; margin: 0; }
  .jump { position: absolute; right: var(--space-3); bottom: var(--space-3); }
</style>
