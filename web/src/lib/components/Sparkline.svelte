<script lang="ts">
  import { sparklinePath } from '../format';

  let { values, tone }: { values: (number | null)[]; tone: 'accent' | 'info' } = $props();
  // No samples at all: a flat line, so the cell still reads as a sparkline.
  const d = $derived(sparklinePath(values) || 'M0,17L60,17');
</script>

<svg width="56" height="18" viewBox="0 0 60 18" preserveAspectRatio="none" aria-hidden="true">
  <path {d} fill="none" stroke={`var(--${tone})`} stroke-width="1.2" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"></path>
</svg>

<style>
  svg { flex: none; }
</style>
