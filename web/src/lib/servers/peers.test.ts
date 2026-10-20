import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { PeerView, ServerSelf } from '$lib/api/types';
import { peers, refreshPeers, selfName, startPeers } from './peers';

const peer = (over: Partial<PeerView> = {}): PeerView => ({
  id: 'b1',
  name: 'mini-02',
  slug: 'mini-02',
  url: 'https://mini-02:8080',
  version: '1.4',
  status: 'reachable',
  stackCount: 2,
  lastSeenAt: null,
  lastError: '',
  compatible: true,
  plaintext: false,
  ...over,
});

function fakeDoc(state: 'visible' | 'hidden' = 'visible') {
  const doc = new EventTarget() as EventTarget & { visibilityState: string };
  doc.visibilityState = state;
  return {
    doc: doc as unknown as Document,
    set(next: 'visible' | 'hidden') {
      doc.visibilityState = next;
      doc.dispatchEvent(new Event('visibilitychange'));
    },
  };
}

describe('peers store', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('polls every 30 s while visible and not while hidden', async () => {
    const list = vi.fn(async () => [peer()]);
    const { doc, set } = fakeDoc();
    const stop = startPeers({ list, doc });
    await vi.advanceTimersByTimeAsync(0);
    expect(list).toHaveBeenCalledTimes(1);
    expect(get(peers)).toEqual({ items: [peer()], loaded: true });

    await vi.advanceTimersByTimeAsync(30_000);
    expect(list).toHaveBeenCalledTimes(2);

    set('hidden');
    await vi.advanceTimersByTimeAsync(120_000);
    expect(list).toHaveBeenCalledTimes(2);

    set('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(list).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(list).toHaveBeenCalledTimes(4);

    stop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(list).toHaveBeenCalledTimes(4);
    expect(get(peers)).toEqual({ items: [], loaded: false });
  });

  it('does not fetch at start while the tab is hidden', async () => {
    const list = vi.fn(async () => [peer()]);
    const { doc } = fakeDoc('hidden');
    const stop = startPeers({ list, doc });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(list).not.toHaveBeenCalled();
    stop();
  });

  it('keeps the last list when a fetch fails and refreshPeers fetches on demand', async () => {
    const list = vi.fn<() => Promise<PeerView[]>>().mockResolvedValueOnce([peer()]).mockRejectedValueOnce(new Error('down'));
    const stop = startPeers({ list, doc: fakeDoc().doc });
    await vi.advanceTimersByTimeAsync(0);
    await refreshPeers();
    expect(list).toHaveBeenCalledTimes(2);
    expect(get(peers).items).toEqual([peer()]);
    stop();
  });
});

describe('this server\'s own name', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('is loaded once, and only when a connected server exists', async () => {
    const self = vi.fn(async () => ({ name: 'ratsrv01' }) as ServerSelf);
    const { doc } = fakeDoc();
    let items: PeerView[] = [];
    const stop = startPeers({ list: async () => items, self, doc });
    await vi.advanceTimersByTimeAsync(0);
    expect(self).not.toHaveBeenCalled();
    expect(get(selfName)).toBeUndefined();

    items = [peer()];
    await vi.advanceTimersByTimeAsync(30_000);
    expect(get(selfName)).toBe('ratsrv01');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(self).toHaveBeenCalledTimes(1);
    stop();
    expect(get(selfName)).toBeUndefined();
  });
});
