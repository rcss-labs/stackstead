<script lang="ts">
  let {
    username,
    onlogout,
    runningOps,
    current = typeof window === 'undefined' ? '/' : window.location.pathname,
  }: {
    username?: string;
    onlogout: () => void;
    runningOps: number;
    /** Path of the page being shown; the router owner passes its current path. */
    current?: string;
  } = $props();

  const links = [
    { href: '/', label: 'Stacks' },
    { href: '/stacks/new', label: 'New stack' },
    { href: '/stacks/import', label: 'Import' },
    { href: '/settings', label: 'Settings' },
  ];
</script>

<nav aria-label="Main" class="nav">
  <ul class="links">
    {#each links as link (link.href)}
      <li><a href={link.href} aria-current={current === link.href ? 'page' : undefined}>{link.label}</a></li>
    {/each}
  </ul>
  <div class="cluster aside">
    {#if runningOps > 0}<span class="ops"><span aria-hidden="true">●</span> {runningOps} running</span>{/if}
    {#if username}<span class="user">{username}</span>{/if}
    <button type="button" onclick={onlogout}>Log out</button>
  </div>
</nav>

<style>
  .nav {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-4);
    border-bottom: 1px solid var(--border);
    background: var(--surface-raised);
  }
  .links { list-style: none; padding: 0; display: flex; flex-wrap: wrap; gap: var(--space-2); }
  a { color: var(--text); text-decoration: none; padding: var(--space-2) var(--space-3); border-radius: var(--radius-md); display: inline-block; }
  a[aria-current='page'] { background: var(--accent); color: var(--accent-contrast); }
  a:not([aria-current]):hover { background: var(--border); }
  .ops { color: var(--info); }
  .user { color: var(--muted); }
  button {
    padding: var(--space-1) var(--space-3);
    background: transparent;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
  }
</style>
