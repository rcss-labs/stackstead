<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    tabs,
    selected,
    onselect,
    children,
  }: {
    tabs: { id: string; label: string }[];
    selected: string;
    onselect: (id: string) => void;
    children?: Snippet<[string]>;
  } = $props();

  const uid = $props.id();
  let buttons = $state<Record<string, HTMLButtonElement | undefined>>({});

  function move(index: number) {
    const target = tabs[(index + tabs.length) % tabs.length];
    onselect(target.id);
    buttons[target.id]?.focus();
  }

  function onkeydown(event: KeyboardEvent, index: number) {
    const next: Record<string, number> = {
      ArrowRight: index + 1,
      ArrowLeft: index - 1,
      Home: 0,
      End: tabs.length - 1,
    };
    if (!(event.key in next)) return;
    event.preventDefault();
    move(next[event.key]);
  }
</script>

<div role="tablist" class="tablist">
  {#each tabs as tab, i (tab.id)}
    <button
      bind:this={buttons[tab.id]}
      type="button"
      role="tab"
      id="{uid}-tab-{tab.id}"
      aria-selected={tab.id === selected}
      aria-controls="{uid}-panel"
      tabindex={tab.id === selected ? 0 : -1}
      onclick={() => onselect(tab.id)}
      onkeydown={(e) => onkeydown(e, i)}>{tab.label}</button
    >
  {/each}
</div>
<div role="tabpanel" id="{uid}-panel" aria-labelledby="{uid}-tab-{selected}" tabindex="0">
  {@render children?.(selected)}
</div>

<style>
  .tablist {
    display: flex;
    gap: var(--space-1);
    overflow-x: auto;
    border-bottom: 1px solid var(--border);
  }
  [role='tab'] {
    padding: var(--space-2) var(--space-4);
    background: none;
    border: 0;
    border-bottom: 3px solid transparent;
    cursor: pointer;
    white-space: nowrap;
  }
  [role='tab'][aria-selected='true'] { border-bottom-color: var(--accent); font-weight: 600; }
  [role='tabpanel'] { padding-block: var(--space-4); }
</style>
