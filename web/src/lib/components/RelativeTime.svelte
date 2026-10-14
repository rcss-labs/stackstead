<script lang="ts">
  import { relativeTime } from '$lib/format';

  let { iso }: { iso: string } = $props();

  let now = $state(new Date());
  $effect(() => {
    const timer = setInterval(() => (now = new Date()), 30_000);
    return () => clearInterval(timer);
  });

  const parsed = $derived(new Date(iso));
  const absolute = $derived(Number.isNaN(parsed.getTime()) ? undefined : parsed.toLocaleString());
</script>

<time datetime={iso} title={absolute}>{relativeTime(iso, now)}</time>
