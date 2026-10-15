<script lang="ts">
  import type { TileModel } from './tiles';

  let { tile }: { tile: TileModel } = $props();
</script>

<div class="tile">
  <div class="label" title={tile.label}>{tile.label}</div>
  <div class="value">{tile.value}</div>
  {#if tile.sub}<div class="sub {tile.subTone ?? 'muted'}" title={tile.sub}>{tile.sub}</div>{/if}
  {#if tile.ratio !== undefined}
    <div class="bar" aria-hidden="true"><span style={`width:${Math.round(tile.ratio * 100)}%`}></span></div>
  {/if}
</div>

<style>
  .tile { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 10px 12px; min-width: 0; }
  .label { font-size: 11.5px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .value { font-family: var(--mono); font-size: 17px; font-weight: 600; margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sub { font-family: var(--mono); font-size: 11px; overflow-wrap: anywhere; }
  .sub.muted { color: var(--muted); }
  .sub.success { color: var(--success); }
  .sub.warning { color: var(--warning); }
  .sub.danger { color: var(--danger); }
  .bar { height: 3px; margin-top: 8px; background: var(--border); border-radius: 2px; overflow: hidden; }
  .bar > span { display: block; height: 100%; background: var(--accent); }
</style>
