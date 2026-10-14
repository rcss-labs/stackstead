<!-- Test-only: a real control spread from Field's ARIA bag, revealed after mount. -->
<script lang="ts">
  import Field from './Field.svelte';

  let { error, reveal = false }: { error?: string; reveal?: boolean } = $props();
</script>

<Field label="Name" id="name" {error} hint="Letters only">
  {#snippet children(aria)}
    {#if reveal}<input aria-describedby={aria['aria-describedby']} aria-invalid={aria['aria-invalid']} id={aria.id} readonly={aria.readonly} />{/if}
  {/snippet}
</Field>
