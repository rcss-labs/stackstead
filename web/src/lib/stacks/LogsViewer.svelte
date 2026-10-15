<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import Button from '../components/Button.svelte';
  import { filterLines, type LogLine } from '../logs/buffer';
  import type { LogError } from '../api/types';
  import type { SSEStatus } from '../sse/client';
  import { effectiveTheme } from '../stores/appearance';
  import { createLogStream, lineTone, type LogSource, type LogStreamOptions } from './logs';

  let { source, services, service: initialService }: { source: LogSource; services: string[]; service?: string } = $props();
  const TAILS = ['100', '200', '1000', 'all'] as const;

  // Unknown names fall back to all services.
  // svelte-ignore state_referenced_locally
  let service = $state(initialService && services.includes(initialService) ? initialService : '');
  let tail = $state<string>('200');
  let follow = $state(true);
  let streamFilter = $state<'' | 'stdout' | 'stderr'>('');
  let searchInput = $state('');
  let search = $state('');
  let paused = $state(false);

  let latest = $state.raw<LogLine[]>([]);
  let frozen = $state.raw<LogLine[]>([]);
  let frozenSeq = $state(0);
  let status = $state<SSEStatus>('connecting');
  let errors = $state.raw<LogError[]>([]);
  let dismissed = $state.raw<Set<string>>(new Set());
  let ended = $state(false);
  let dropped = $state(0);

  function currentOptions(): LogStreamOptions {
    return { service: service || undefined, tail: tail === 'all' ? 'all' : Number(tail), follow };
  }

  // The viewer is keyed by source in the page; a different source remounts it.
  // svelte-ignore state_referenced_locally
  const stream = createLogStream(source, currentOptions());
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
    paused = false;
    frozen = [];
    frozenSeq = 0;
    dismissed = new Set();
    stream.restart(currentOptions());
  }

  const view = $derived(paused ? frozen : latest);
  const visible = $derived(filterLines(view, { stream: streamFilter || undefined, search: search || undefined }));
  const newLines = $derived(paused ? Math.max(0, (latest.at(-1)?.seq ?? 0) - frozenSeq) : 0);
  const visibleErrors = $derived(errors.filter((e) => !dismissed.has(e.container + '\u0000' + e.error)));

  function togglePause() {
    if (!paused) {
      frozen = latest;
      frozenSeq = latest.at(-1)?.seq ?? 0;
    }
    paused = !paused;
    if (!paused) void scrollToEnd();
  }

  function clear() {
    stream.clear();
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

  function segments(text: string, needle: string): { text: string; match: boolean }[] {
    const clean = text;
    if (!needle) return [{ text: clean, match: false }];
    const lower = clean.toLowerCase();
    const n = needle.toLowerCase();
    // toLowerCase can change length (e.g. U+0130); offsets would be wrong, so skip highlighting.
    if (lower.length !== clean.length) return [{ text: clean, match: false }];
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

  const statusTone = $derived(
    ended || status === 'unauthorized' || status === 'gone' || status === 'closed'
      ? 'muted'
      : paused || status !== 'open'
        ? 'warning'
        : 'live',
  );

  function hue(name: string): number {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
    return h;
  }
  function serviceColor(name: string): string {
    return $effectiveTheme === 'dark' ? `hsl(${hue(name)} 60% 65%)` : `hsl(${hue(name)} 60% 42%)`;
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
        ? paused
          ? 'paused'
          : follow
            ? 'live · following'
            : 'live'
        : status === 'connecting'
          ? 'connecting'
          : status === 'reconnecting'
            ? 'reconnecting'
            : status === 'unauthorized'
              ? 'session expired - sign in again'
              : status === 'gone'
                ? 'stack or logs no longer available'
                : 'closed',
  );
</script>

<section class="logs" aria-label="Logs">
  <div class="toolbar">
    {#if services.length > 0}
      <select aria-label="Service" bind:value={service} onchange={restart}>
        <option value="">All services</option>
        {#each services as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    {/if}
    <select aria-label="Stream" bind:value={streamFilter}>
      <option value="">stdout + stderr</option>
      <option value="stdout">stdout</option>
      <option value="stderr">stderr</option>
    </select>
    <select aria-label="Tail" bind:value={tail} onchange={restart}>
      {#each TAILS as t (t)}<option value={t}>{t === 'all' ? 'Tail all' : `Tail ${t}`}</option>{/each}
    </select>
    <label class="chip"><input type="checkbox" bind:checked={follow} onchange={restart} /> Follow</label>
    <input class="search" type="search" aria-label="Search logs" placeholder="Search" bind:value={searchInput} oninput={onSearchInput} />
    <small class="status {statusTone}" role="status" aria-live="polite">{statusLabel}</small>
    <Button variant="secondary" size="sm" onclick={togglePause}>{paused ? 'Resume' : 'Pause'}</Button>
    <Button variant="ghost" size="sm" onclick={clear}>Clear</Button>
    {#if ended}<Button variant="secondary" size="sm" onclick={restart}>Reconnect</Button>{/if}
  </div>

  {#if tail === 'all'}
    <p class="note warn">Loading the entire log history can be slow and large.</p>
  {/if}
  {#if dropped > 0}
    <p class="note">{dropped} older lines dropped</p>
  {/if}
  {#each visibleErrors as e (e.container + e.error)}
    <div class="err" role="alert">
      <strong>{e.service} ({e.container})</strong>
      <small>{e.error}</small>
      <button type="button" aria-label="Dismiss error for {e.container}" onclick={() => (dismissed = new Set([...dismissed, e.container + '\u0000' + e.error]))}>Dismiss</button>
    </div>
  {/each}

  <!-- svelte-ignore a11y_no_noninteractive_tabindex (keyboard-scrollable log region) -->
  <div class="scroller" aria-live="off" bind:this={scroller} onscroll={onScroll} tabindex="0" role="log" aria-label="Log output">
    {#each visible as line (line.seq)}
      <div class="row {lineTone(line.text) ?? ''}" data-testid="log-row">
        <time datetime={line.time} title={utc(line.time)}>{clock(line.time)}</time>
        <small class="svc" style="color: {serviceColor(line.service)}">{line.service}</small>
        <small class="strm {line.stream}">{line.stream}</small>
        <span class="txt">{#each segments(line.text, search) as seg, i (i)}{#if seg.match}<mark>{seg.text}</mark>{:else}{seg.text}{/if}{/each}</span>
      </div>
    {/each}
    {#if ended}<p class="note">end of log</p>{/if}
  </div>

  {#if paused && newLines > 0}
    <div class="newbar">
      <small>{newLines} new lines</small>
      <Button variant="secondary" size="sm" onclick={togglePause}>Jump to latest</Button>
    </div>
  {/if}
</section>

<style>
  .logs { display: flex; flex-direction: column; gap: var(--space-2); min-width: 0; }
  .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 12px; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); }
  select, .search {
    height: 26px; padding: 0 8px; background: var(--raised); color: var(--text);
    border: 1px solid var(--border-input); border-radius: var(--radius-sm); font: inherit; font-size: 12px;
  }
  .search { width: 180px; }
  .chip {
    display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 8px; font-size: 12px;
    background: var(--raised); border: 1px solid var(--border-input); border-radius: var(--radius-pill);
  }
  .status { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--muted); }
  .status.live { color: var(--success); }
  .status.warning { color: var(--warning); }
  .status.live::before {
    content: ''; width: 7px; height: 7px; border-radius: 50%; background: var(--success);
    animation: pulse 1.6s ease-in-out infinite;
  }
  @keyframes pulse { 50% { opacity: 0.35; } }
  @media (prefers-reduced-motion: reduce) { .status.live::before { animation: none; } }
  .note { margin: 0; color: var(--muted); font-size: 0.85rem; }
  .note.warn { color: var(--warning); }
  .err { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; color: var(--danger); }
  .scroller {
    height: clamp(280px, 60vh, 720px); overflow: auto; background: var(--bg); border: 1px solid var(--border);
    border-radius: var(--radius-md); padding: 4px 0; font-family: var(--mono); font-size: 11.5px; line-height: 18px;
  }
  .row {
    display: grid; grid-template-columns: 72px 90px 44px minmax(0, 1fr); gap: 10px; padding: 0 12px; align-items: baseline;
  }
  .row:hover { background: var(--surface); }
  .row time, .row small { color: var(--faint); font-size: inherit; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .svc { font-weight: 600; }
  .strm.stderr { color: var(--danger); }
  .txt { white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; }
  .row.danger .txt { color: var(--danger); }
  .row.warning .txt { color: var(--warning); }
  .newbar { display: flex; gap: var(--space-2); align-items: center; }
</style>
