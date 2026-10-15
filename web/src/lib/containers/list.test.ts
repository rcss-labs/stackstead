import { get, writable } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ContainerRow } from '../api/types';
import type { LiveChange } from '../stores/live';
import { chipCounts, createContainerList, filterContainers, isProblem, sortContainers } from './list';

function row(name: string, over: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: name.padEnd(12, '0').slice(0, 12),
    name,
    image: 'nginx:1',
    state: 'running',
    status: 'Up 1 hour',
    health: 'none',
    exitCode: null,
    createdAt: '2026-01-01T00:00:00Z',
    project: '',
    service: '',
    stack: '',
    managed: 'none',
    ports: [],
    networks: {},
    cpu: null,
    mem: null,
    memLimit: null,
    restarts24h: 0,
    ...over,
  };
}

describe('isProblem', () => {
  it.each([
    [{ state: 'running' }, false],
    [{ state: 'running', health: 'healthy' }, false],
    [{ state: 'running', health: 'starting' }, false],
    [{ state: 'running', health: 'unhealthy' }, true],
    [{ state: 'restarting' }, true],
    [{ state: 'dead' }, true],
    [{ state: 'exited', exitCode: 0 }, false],
    [{ state: 'exited', exitCode: null }, false],
    [{ state: 'exited', exitCode: 137 }, true],
    [{ state: 'created' }, false],
    [{ state: 'paused' }, false],
  ] as [Partial<ContainerRow>, boolean][])('%j -> %s', (over, expected) => {
    expect(isProblem(row('c', over))).toBe(expected);
  });
});

describe('sortContainers', () => {
  it('orders problems, running, stopped, then by name, without mutating the input', () => {
    const input = [
      row('stopped-b', { state: 'exited', exitCode: 0 }),
      row('run-b'),
      row('bad-z', { state: 'restarting' }),
      row('run-a'),
      row('stopped-a', { state: 'exited', exitCode: 0 }),
      row('bad-a', { health: 'unhealthy' }),
    ];
    const names = sortContainers(input).map((c) => c.name);
    expect(names).toEqual(['bad-a', 'bad-z', 'run-a', 'run-b', 'stopped-a', 'stopped-b']);
    expect(input[0].name).toBe('stopped-b');
  });
});

describe('filterContainers and chipCounts', () => {
  const rows = [
    row('web', { managed: 'stack', stack: 'shop', project: 'shop-proj', image: 'nginx:1', ports: ['0.0.0.0:8080->80/tcp'] }),
    row('db', { managed: 'compose', project: 'legacy', image: 'postgres:16' }),
    row('tool', { state: 'exited', exitCode: 0, status: 'Exited (0)' }),
    row('crash', { state: 'exited', exitCode: 1, managed: 'stack', stack: 'shop' }),
  ];

  it('filters by chip', () => {
    const names = (chip: Parameters<typeof filterContainers>[1]) => filterContainers(rows, chip, '').map((c) => c.name);
    expect(names('all')).toEqual(['web', 'db', 'tool', 'crash']);
    expect(names('running')).toEqual(['web', 'db']);
    expect(names('stopped')).toEqual(['tool', 'crash']);
    expect(names('problems')).toEqual(['crash']);
    expect(names('unmanaged')).toEqual(['db', 'tool']);
  });

  it.each([
    ['WEB', ['web']],
    ['postgres', ['db']],
    ['shop-proj', ['web']],
    ['legacy', ['db']],
    ['shop', ['web', 'crash']],
    ['8080', ['web']],
    ['  nope ', []],
  ])('searches %s', (query, expected) => {
    expect(filterContainers(rows, 'all', query).map((c) => c.name)).toEqual(expected);
  });

  it('combines chip and query', () => {
    expect(filterContainers(rows, 'stopped', 'shop').map((c) => c.name)).toEqual(['crash']);
  });

  it('counts every chip', () => {
    expect(chipCounts(rows)).toEqual({ all: 4, running: 2, stopped: 2, problems: 1, unmanaged: 2 });
  });
});

class FakeDoc extends EventTarget {
  visibilityState: 'visible' | 'hidden' = 'visible';
  setVisibility(v: 'visible' | 'hidden') {
    this.visibilityState = v;
    this.dispatchEvent(new Event('visibilitychange'));
  }
}

describe('createContainerList', () => {
  let doc: FakeDoc;
  let changes: ReturnType<typeof writable<LiveChange | null>>;
  let list: ReturnType<typeof vi.fn<() => Promise<ContainerRow[]>>>;
  const make = () => createContainerList({ list, changes, doc: doc as unknown as Document });
  const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);
  const one = [row('a')];

  beforeEach(() => {
    vi.useFakeTimers();
    doc = new FakeDoc();
    changes = writable<LiveChange | null>(null);
    list = vi.fn(async () => one);
  });
  afterEach(() => vi.useRealTimers());

  it('does nothing before start', async () => {
    make();
    await tick(60_000);
    expect(list).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('fetches on start and leaves rows undefined until then', async () => {
    const c = make();
    expect(get(c.rows)).toBeUndefined();
    c.start();
    await tick();
    expect(list).toHaveBeenCalledTimes(1);
    expect(get(c.rows)).toBe(one);
    c.stop();
  });

  it('refetches every 10 s while visible', async () => {
    const c = make();
    c.start();
    await tick();
    await tick(10_000);
    await tick(10_000);
    expect(list).toHaveBeenCalledTimes(3);
    c.stop();
  });

  it('debounces changes by 300 ms and fetches resync immediately', async () => {
    const c = make();
    c.start();
    await tick();
    changes.set({ project: 'x', seq: 1 });
    await tick(299);
    expect(list).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(list).toHaveBeenCalledTimes(2);
    changes.set({ project: '', seq: 2, resync: true });
    await tick();
    expect(list).toHaveBeenCalledTimes(3);
    c.stop();
  });

  it('pauses while hidden and fetches immediately when visible again', async () => {
    const c = make();
    c.start();
    await tick();
    doc.setVisibility('hidden');
    expect(vi.getTimerCount()).toBe(0);
    changes.set({ project: 'x', seq: 1 });
    await tick(60_000);
    expect(list).toHaveBeenCalledTimes(1);
    doc.setVisibility('visible');
    await tick();
    expect(list).toHaveBeenCalledTimes(2);
    await tick(10_000);
    expect(list).toHaveBeenCalledTimes(3);
    c.stop();
  });

  it('starts no timers and no fetch when hidden at start', async () => {
    doc.visibilityState = 'hidden';
    const c = make();
    c.start();
    await tick(60_000);
    expect(list).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
    c.stop();
  });

  it('stop clears timers and ignores a late response', async () => {
    let resolve!: (r: ContainerRow[]) => void;
    list.mockImplementationOnce(() => new Promise((r) => (resolve = r)));
    const c = make();
    c.start();
    c.stop();
    expect(vi.getTimerCount()).toBe(0);
    resolve(one);
    await tick();
    expect(get(c.rows)).toBeUndefined();
  });

  it('ignores a stale response that lands after a newer fetch', async () => {
    const resolvers: ((r: ContainerRow[]) => void)[] = [];
    list.mockImplementation(() => new Promise((r) => resolvers.push(r)));
    const c = make();
    c.start();
    c.refresh();
    resolvers[1]([row('new')]);
    await tick();
    resolvers[0]([row('old')]);
    await tick();
    expect(get(c.rows)?.map((r) => r.name)).toEqual(['new']);
    c.stop();
  });

  it('patch replaces the row with the same id and drops a fetch that started before it', async () => {
    let resolve!: (r: ContainerRow[]) => void;
    const c = make();
    c.start();
    await tick();
    list.mockImplementationOnce(() => new Promise((r) => (resolve = r)));
    c.refresh();
    c.patch(row('a', { state: 'exited', exitCode: 0 }));
    expect(get(c.rows)?.[0].state).toBe('exited');
    resolve([row('a')]); // pre-action data landing late
    await tick();
    expect(get(c.rows)?.[0].state).toBe('exited');
    c.stop();
  });

  it('skips an interval tick while a fetch is in flight', async () => {
    list.mockImplementation(() => new Promise(() => {}));
    const c = make();
    c.start();
    await tick(30_000);
    expect(list).toHaveBeenCalledTimes(1);
    c.stop();
  });

  it('keeps the previous rows on failure, reports the message, and clears it on success', async () => {
    const c = make();
    c.start();
    await tick();
    list.mockRejectedValueOnce(new Error('docker down'));
    c.refresh();
    await tick();
    expect(get(c.rows)).toBe(one);
    expect(get(c.error)).toBe('docker down');
    c.refresh();
    await tick();
    expect(get(c.error)).toBeUndefined();
    c.stop();
  });
});
