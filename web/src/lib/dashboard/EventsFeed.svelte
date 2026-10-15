<script lang="ts">
  import type { DockerEvent } from '$lib/api/types';
  import { link } from '$lib/router';
  import type { SSEStatus } from '$lib/sse/client';

  let { events, connection }: { events: DockerEvent[]; connection: SSEStatus } = $props();

  const tone = (action: string) => {
    if (['die', 'oom', 'kill', 'unhealthy'].includes(action)) return 'danger';
    if (['stop', 'pause', 'restart'].includes(action)) return 'warning';
    if (['start', 'create', 'healthy'].includes(action)) return 'success';
    return 'muted';
  };
  // health_status events carry the outcome in `health`; show that instead of the raw action.
  const actionOf = (e: DockerEvent) => (e.action === 'health_status' && e.health ? e.health : e.action);
  const clock = (iso: string) => {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const two = (n: number) => String(n).padStart(2, '0');
    return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
  };
  const subject = (e: DockerEvent) => (e.action === 'die' && e.exitCode !== undefined ? `${e.container} exit ${e.exitCode}` : e.container);
</script>

<section class="card">
  <h3>
    Docker events
    {#if connection === 'open'}
      <span class="status ok"><i class="live" aria-hidden="true"></i>live</span>
    {:else}
      <span class="status warn">reconnecting</span>
    {/if}
  </h3>
  {#if events.length === 0}
    <p class="empty">Waiting for events…</p>
  {:else}
    <!-- Deliberately not a live region: a busy host would make it chatter. -->
    <ul>
      {#each events as e, i (`${e.time}|${e.container}|${e.action}|${i}`)}
        <li>
          <span class="time">{clock(e.time)}</span>
          <span class="action {tone(actionOf(e))}" title={actionOf(e)}>{actionOf(e)}</span>
          <a class="subject" href={`/containers/${encodeURIComponent(e.container)}`} title={subject(e)} use:link>{subject(e)}</a>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 10px 12px; min-width: 0; }
  h3 { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin: 0 0 8px; font-size: 13px; font-weight: 600; }
  .status { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-weight: 400; }
  .status.ok { color: var(--success); }
  .status.warn { color: var(--warning); }
  .live { width: 6px; height: 6px; border-radius: 50%; background: var(--success); animation: pulse 2s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
  .empty { margin: 0; color: var(--muted); font-size: 12.5px; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; font-family: var(--mono); font-size: 11.5px; }
  li { display: grid; grid-template-columns: 62px 58px 1fr; gap: 6px; min-width: 0; }
  .time { color: var(--faint); }
  .action { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .action.danger { color: var(--danger); }
  .action.warning { color: var(--warning); }
  .action.success { color: var(--success); }
  .action.muted { color: var(--muted); }
  .subject { color: inherit; text-decoration: none; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .subject:hover { color: var(--accent); }
</style>
