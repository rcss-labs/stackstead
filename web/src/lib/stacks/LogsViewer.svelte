<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import Button from '../components/Button.svelte';
  import { filterLines, type LogLine } from '../logs/buffer';
  import type { LogError } from '../api/types';
  import type { SSEStatus } from '../sse/client';
  import { createLogStream, type LogStreamOptions } from './logs';

  let { stackName, services }: { stackName: string; services: string[] } = $props();

  const uid = $props.id();
  const TAILS = ['100', '200', '1000', 'all'] as const;

  let service = $state('');
  let tail = $state<string>('200');
  let follow = $state(true);
  let streamFilter = $state<'' | 'stdout' | 'stderr'>('');
  let searchInput = $state('');
  let search = $state('');
  let paused = $state(false);

  let latest = $state.raw<LogLine[]>([]);
  let frozen = $state.raw<LogLine[]>([]);
  let status = $state<SSEStatus>('connecting');
  let errors = $state.raw<LogError[]>([]);
  let dismissed = $state.raw<Set<string>>(new Set());
  let ended = $state(false);
  let dropped = $state(0);

  function currentOptions(): LogStreamOptions {
    return { service: service || undefined, tail: tail === 'all' ? 'all' : Number(tail), follow };
  }

  // The viewer is keyed by stack in the page; a different stack remounts it.
  // svelte-ignore state_referenced_locally
  const stream = createLogStream(stackName, currentOptions());
  const unsubs = [
    stream.buffer.subscribe((l) => (latest = l)),
    stream.status.subscribe((s) => (status = s)),
    stream.errors.subscribe((e) => (errors = e)),
    stream.ended.subscribe((e) => (ended = e)),
    stream.dropped.subscribe((d) => (dropped = d)),
  ];
  onDestroy(() => {
    stream.close();
    unsubs.forEach((u) => u());
    clearTimeout(debounce);
  });

  let debounce: ReturnType<typeof setTimeout> | undefined;
  function onSearchInput() {
    clearTimeout(debounce);
    debounce = setTimeout(() => (search = searchInput), 150);
  }

  function restart() {
    dismissed = new Set();
    stream.restart(currentOptions());
  }

  const view = $derived(paused ? frozen : latest);
  const visible = $derived(filterLines(view, { stream: streamFilter || undefined, search: search || undefined }));
  const newLines = $derived(paused ? Math.max(0, (latest.at(-1)?.seq ?? 0) - (frozen.at(-1)?.seq ?? 0)) : 0);
  const visibleErrors = $derived(errors.filter((e) => !dismissed.has(e.container + '\u0000' + e.error)));

  function togglePause() {
    if (!paused) frozen = latest;
    paused = !paused;
    if (!paused) void scrollToEnd();
  }

  function clear() {
    stream.buffer.clear();
    frozen = [];
  }

  let scroller: HTMLDivElement | undefined = $state();
  let stick = true;
  function onScroll() {
    if (!scroller) return;
    stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 24;
  }
  async function scrollToEnd() {
    stick = true;
    await tick();
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }
  $effect(() => {
    void visible;
    if (stick && !paused) void scrollToEnd();
  });

  // eslint-disable-next-line no-control-regex
  const ANSI = /\u001b\[[0-9;?]*[ -/]*[@-~]/g;
  function segments(text: string, needle: string): { text: string; match: boolean }[] {
    const clean = text.replace(ANSI, '');
    if (!needle) return [{ text: clean, match: false }];
    const lower = clean.toLowerCase();
    const n = needle.toLowerCase();
    const out: { text: string; match: boolean }[] = [];
    let i = 0;
    for (;;) {
      const at = lower.indexOf(n, i);
      if (at < 0) break;
      if (at > i) out.push({ text: clean.slice(i, at), match: false });
      out.push({ text: clean.slice(at, at + n.length), match: true });
      i = at + n.length;
    }
    if (i < clean.length) out.push({ text: clean.slice(i), match: false });
    return out;
  }

  function hue(name: string): number {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
    return h;
  }
  function clock(iso: string): string {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
  }
  function utc(iso: string): string {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toISOString();
  }

  const statusLabel = $derived(
    ended
      ? 'ended'
      : status === 'open'
        ? 'live'
        : status === 'connecting'
          ? 'connecting'
          : status === 'reconnecting'
            ? 'reconnecting'
            : status,
  );
</script>

<section class="logs" aria-label="Logs">
  <div class="toolbar">
    <label for="{uid}-svc">Service</label>
    <select id="{uid}-svc" bind:value={service} onchange={restart}>
      <option value="">All</option>
      {#each services as s (s)}<option value={s}>{s}</option>{/each}
    </select>
    <label for="{uid}-tail">Tail</label>
    <select id="{uid}-tail" bind:value={tail} onchange={restart}>
      {#each TAILS as t (t)}<option value={t}>{t}</option>{/each}
    </select>
    <label class="check"><input type="checkbox" bind:checked={follow} onchange={restart} /> Follow</label>
    <label for="{uid}-stream">Stream</label>
    <select id="{uid}-stream" bind:value={streamFilter}>
      <option value="">All</option>
      <option value="stdout">stdout</option>
      <option value="stderr">stderr</option>
    </select>
    <label for="{uid}-search">Search logs</label>
    <input id="{uid}-search" type="search" bind:value={searchInput} oninput={onSearchInput} />
    <Button variant="secondary" onclick={togglePause}>{paused ? 'Resume' : 'Pause'}</Button>
    <Button variant="ghost" onclick={clear}>Clear</Button>
    <small class="status" role="status" aria-live="polite">{statusLabel}</small>
  </div>

  {#if tail === 'all'}
    <p class="note warn">Loading the entire log history can be slow and large.</p>
  {/if}
  {#if dropped > 0}
    <p class="note">{dropped} older lines dropped</p>
  {/if}
  {#each visibleErrors as e (e.container + e.error)}
    <div class="err" role="alert">
      <strong>{e.service} ({e.container.slice(0, 12)})</strong>
      <small>{e.error}</small>
      <button type="button" onclick={() => (dismissed = new Set([...dismissed, e.container + '\u0000' + e.error]))}>Dismiss</button>
    </div>
  {/each}

  <!-- svelte-ignore a11y_no_noninteractive_tabindex (keyboard-scrollable log region) -->
  <div class="scroller" bind:this={scroller} onscroll={onScroll} tabindex="0" role="log" aria-label="Log output">
    {#each visible as line (line.seq)}
      <div class="row" data-testid="log-row">
        <time datetime={line.time} title={utc(line.time)}>{clock(line.time)}</time>
        <small class="svc" style="color: hsl({hue(line.service)} 65% 50%)">{line.service}</small>
        <small class="strm {line.stream}">{line.stream}</small>
        <span class="txt">{#each segments(line.text, search) as seg, i (i)}{#if seg.match}<mark>{seg.text}</mark>{:else}{seg.text}{/if}{/each}</span>
      </div>
    {/each}
    {#if ended}<p class="note">end of log</p>{/if}
  </div>

  {#if paused && newLines > 0}
    <div class="newbar">
      <small>{newLines} new lines</small>
      <Button variant="secondary" onclick={togglePause}>Jump to latest</Button>
    </div>
  {/if}
</section>

<style>
  .logs { display: flex; flex-direction: column; gap: var(--space-2); min-width: 0; }
  .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
  .check { display: inline-flex; align-items: center; gap: var(--space-1); }
  .note { margin: 0; color: var(--muted); font-size: 0.85rem; }
  .note.warn { color: var(--warning); }
  .err { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; color: var(--danger); }
  .scroller {
    height: 60vh; overflow: auto; background: var(--surface-raised); border: 1px solid var(--border);
    border-radius: var(--radius-md); padding: var(--space-2); font-family: var(--font-mono); font-size: 0.8rem;
  }
  .row { display: flex; gap: var(--space-2); align-items: baseline; }
  .row time, .row small { flex: none; color: var(--muted); }
  .svc { font-weight: 600; }
  .strm.stderr { color: var(--danger); }
  .txt { white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; }
  .newbar { display: flex; gap: var(--space-2); align-items: center; }
</style>
