<script lang="ts">
  import { link } from '../router';
  import { effectiveTheme, setTheme } from '../stores/appearance';
  import { ICONS } from './icons';

  let {
    username,
    onlogout,
    current,
  }: {
    username?: string;
    onlogout: () => void;
    /** Path of the page being shown; the router owner passes its current path. */
    current: string;
  } = $props();

  const items: { href: string; label: string; icon: 'stacks' | 'network' | 'box' | 'plus' | 'import' | 'settings'; match: (p: string) => boolean }[] = [
    { href: '/', label: 'Stacks', icon: 'stacks', match: (p) => p === '/' || /^\/stacks\/(?!new$|import$)[^/]+$/.test(p) },
    { href: '/containers', label: 'Containers', icon: 'box', match: (p) => p === '/containers' || p.startsWith('/containers/') },
    { href: '/networks', label: 'Networks', icon: 'network', match: (p) => p === '/networks' },
    { href: '/stacks/new', label: 'New stack', icon: 'plus', match: (p) => p === '/stacks/new' },
    { href: '/stacks/import', label: 'Import', icon: 'import', match: (p) => p === '/stacks/import' },
    { href: '/settings', label: 'Settings', icon: 'settings', match: (p) => p === '/settings' },
  ];

  const dark = $derived($effectiveTheme === 'dark');
  const initials = $derived((username ?? '').slice(0, 2).toUpperCase());
</script>

<nav aria-label="Main" class="rail">
  <a class="logo" href="/" use:link aria-label="Stackstead"><img src="/containers.png" alt="" width="34" height="34" /></a>
  {#each items as item (item.href)}
    <a
      class="item"
      href={item.href}
      use:link
      aria-label={item.label}
      title={item.label}
      aria-current={item.match(current) ? 'page' : undefined}
    >
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={ICONS[item.icon]}></path></svg>
    </a>
  {/each}
  <div class="bottom">
    <button
      type="button"
      class="item"
      aria-label={dark ? 'Switch to light theme' : 'Switch to dark theme'}
      title={dark ? 'Switch to light theme' : 'Switch to dark theme'}
      onclick={() => setTheme(dark ? 'light' : 'dark')}
    >
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={dark ? ICONS.sun : ICONS.moon}></path></svg>
    </button>
    <button type="button" class="avatar" aria-label="Log out" title={`${username ?? ''} · Log out`} onclick={onlogout}>{initials}</button>
  </div>
</nav>

<style>
  .rail {
    width: 52px;
    flex: none;
    box-sizing: border-box;
    background: var(--surface);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 10px 0;
    gap: 4px;
    position: sticky;
    top: 0;
    height: 100vh;
  }
  .logo { width: 36px; height: 36px; display: grid; place-items: center; margin-bottom: 10px; }
  .logo img { width: 34px; height: 34px; object-fit: contain; display: block; }
  .item {
    width: 36px;
    height: 36px;
    border-radius: 7px;
    border: 0;
    background: transparent;
    color: var(--muted);
    display: grid;
    place-items: center;
    cursor: pointer;
    text-decoration: none;
    padding: 0;
  }
  .item:hover { background: var(--hover); color: var(--text); }
  .item[aria-current='page'] { background: var(--accent-soft); color: var(--accent); }
  .bottom { margin-top: auto; display: flex; flex-direction: column; align-items: center; gap: 4px; }
  .avatar {
    width: 28px;
    height: 28px;
    border-radius: 50%;
    background: var(--raised);
    border: 1px solid var(--border-strong);
    color: var(--text);
    font: 600 11px var(--sans);
    cursor: pointer;
    padding: 0;
  }
</style>
