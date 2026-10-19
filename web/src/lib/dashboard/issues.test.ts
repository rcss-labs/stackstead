import { describe, expect, it } from 'vitest';
import type { Alert, StorageKindTotals, StorageOverview } from '../api/types';
import { EXCLUDED_ALERT_KINDS, RECLAIMABLE_CARD_MIN, failingServices, issueCards, reclaimableTotal } from './issues';

const GIB = 1024 ** 3;

function alert(over: Partial<Alert> = {}): Alert {
  return {
    kind: 'exited',
    severity: 'danger',
    stack: 'web',
    service: 'app',
    message: 'app exited (1)',
    since: '2026-10-19T10:00:00Z',
    ...over,
  };
}

function totals(reclaimable: number | null): StorageKindTotals {
  return {
    count: 1,
    size: null,
    reclaimable,
    sizesAt: null,
    refreshing: false,
    error: '',
  };
}

function storage(images: number | null, containers: number | null, volumes: number | null, buildCache: number | null): StorageOverview {
  return {
    images: totals(images),
    containers: totals(containers),
    volumes: totals(volumes),
    buildCache: totals(buildCache),
    topContainers: [],
    declarationsUnknown: [],
  };
}

describe('issueCards', () => {
  it('is undefined until alerts are loaded, and empty with no alerts and nothing reclaimable', () => {
    expect(issueCards({ alerts: undefined, storage: storage(GIB, 0, 0, 0) })).toBeUndefined();
    expect(issueCards({ alerts: [], storage: undefined })).toEqual([]);
    expect(issueCards({ alerts: [], storage: storage(0, 0, 0, 0) })).toEqual([]);
  });

  it('every documented kind maps to its primary action and title', () => {
    const rows: [string, string, unknown][] = [
      ['exited', 'app exited', { label: 'Up stack', kind: 'act', stack: 'web', action: 'up' }],
      [
        'unhealthy',
        'app is unhealthy',
        {
          label: 'Restart stack',
          kind: 'act',
          stack: 'web',
          action: 'restart',
        },
      ],
      [
        'restart_loop',
        'app is restart looping',
        {
          label: 'Restart stack',
          kind: 'act',
          stack: 'web',
          action: 'restart',
        },
      ],
      ['port_clash', 'Port conflict', { label: 'Open stack', kind: 'navigate', href: '/stacks/web' }],
      ['override', 'Override file ignored', { label: 'Open stack', kind: 'navigate', href: '/stacks/web' }],
      [
        'parse_error',
        'Compose file cannot be parsed',
        {
          label: 'Open compose',
          kind: 'navigate',
          href: '/stacks/web?tab=compose',
        },
      ],
      ['something_new', 'something new', { label: 'Open stack', kind: 'navigate', href: '/stacks/web' }],
    ];
    for (const [kind, title, primary] of rows) {
      const [card] = issueCards({
        alerts: [alert({ kind })],
        storage: undefined,
      })!;
      expect(card.title, kind).toBe(title);
      expect(card.primary, kind).toEqual(primary);
      expect(card.kind).toBe(kind);
    }
    expect([...EXCLUDED_ALERT_KINDS].sort()).toEqual(['docker_disconnected', 'metrics_degraded']);
    const cards = issueCards({
      alerts: [alert({ kind: 'docker_disconnected' }), alert({ kind: 'metrics_degraded' }), alert({ kind: 'override' })],
      storage: undefined,
    })!;
    expect(cards.map((c) => c.kind)).toEqual(['override']);
  });

  it('carries message, since, tone and where from the alert, in server order', () => {
    const cards = issueCards({
      alerts: [alert({ severity: 'warning', message: 'first', since: 's1' }), alert({ severity: 'info', message: 'second', since: 's2' })],
      storage: undefined,
    })!;
    expect(cards.map((c) => [c.message, c.since, c.tone])).toEqual([
      ['first', 's1', 'warning'],
      ['second', 's2', 'info'],
    ]);
    expect(cards[0].where).toEqual({
      label: 'web',
      href: '/stacks/web',
      mono: true,
    });
    expect(new Set(cards.map((c) => c.id)).size).toBe(2);
  });

  it('unhealthy pulses, exited does not', () => {
    const cards = issueCards({
      alerts: [alert({ kind: 'unhealthy' }), alert({ kind: 'exited' }), alert({ kind: 'restart_loop' })],
      storage: undefined,
    })!;
    expect(cards.map((c) => c.pulse)).toEqual([true, false, false]);
  });

  it('Open logs only when stack and service are known', () => {
    const [both, noService, noStack] = issueCards({
      alerts: [alert(), alert({ service: undefined }), alert({ stack: undefined, container: 'c1' })],
      storage: undefined,
    })!;
    expect(both.secondary).toEqual({
      label: 'Open logs',
      kind: 'navigate',
      href: '/stacks/web?tab=logs&service=app',
    });
    expect(noService.secondary).toBeUndefined();
    expect(noStack.secondary).toBeUndefined();
  });

  it('a container-only alert links to the container and never acts', () => {
    for (const kind of ['exited', 'unhealthy', 'restart_loop']) {
      const [card] = issueCards({
        alerts: [
          alert({
            kind,
            stack: undefined,
            service: undefined,
            container: 'lone one',
          }),
        ],
        storage: undefined,
      })!;
      expect(card.where).toEqual({
        label: 'lone one',
        href: '/containers/lone%20one',
        mono: true,
      });
      expect(card.primary).toEqual({
        label: 'Open container',
        kind: 'navigate',
        href: '/containers/lone%20one',
      });
    }
  });

  it('names with spaces and # are encoded in hrefs', () => {
    const [card] = issueCards({
      alerts: [alert({ kind: 'port_clash', stack: 'my #stack', service: 'a b' })],
      storage: undefined,
    })!;
    expect(card.where?.href).toBe('/stacks/my%20%23stack');
    expect(card.primary).toEqual({
      label: 'Open stack',
      kind: 'navigate',
      href: '/stacks/my%20%23stack',
    });
    expect(card.secondary).toEqual({
      label: 'Open logs',
      kind: 'navigate',
      href: '/stacks/my%20%23stack?tab=logs&service=a%20b',
    });
  });

  it('the storage card sums reclaimable, lists only positive kinds, sorts last, appears at exactly 1 GiB and is absent below', () => {
    expect(RECLAIMABLE_CARD_MIN).toBe(GIB);
    const cards = issueCards({
      alerts: [alert()],
      storage: storage(GIB - 1048576, 1048576, 0, null),
    })!;
    expect(cards).toHaveLength(2);
    const card = cards[1];
    expect(card).toMatchObject({
      kind: 'reclaimable',
      tone: 'info',
      pulse: false,
      where: { label: 'storage', href: '/storage', mono: false },
      title: '1.0 GiB can be freed',
      message: '1023.0 MiB dangling images · 1.0 MiB container layers',
      primary: { label: 'Review cleanup', kind: 'navigate', href: '/storage' },
    });
    expect(card.since).toBeUndefined();
    expect(card.secondary).toBeUndefined();

    const all = issueCards({
      alerts: [],
      storage: storage(GIB, 2048, 3072, 4096),
    })![0];
    expect(all.message).toBe('1.0 GiB dangling images · 2.0 KiB container layers · 3.0 KiB unused volumes · 4.0 KiB build cache');

    expect(issueCards({ alerts: [], storage: storage(GIB - 1, 0, 0, 0) })).toEqual([]);
    expect(issueCards({ alerts: [], storage: storage(null, null, null, null) })).toEqual([]);
  });
});

describe('reclaimableTotal', () => {
  it('treats null as 0', () => {
    expect(reclaimableTotal(undefined)).toBe(0);
    expect(reclaimableTotal(storage(null, 5, null, 7))).toBe(12);
  });
});

describe('failingServices', () => {
  it('is distinct, per stack, and ignores non-container kinds', () => {
    const alerts = [
      alert({ kind: 'exited', service: 'b' }),
      alert({ kind: 'unhealthy', service: 'a' }),
      alert({ kind: 'restart_loop', service: 'b' }),
      alert({ kind: 'port_clash', service: 'z' }),
      alert({ kind: 'exited', stack: 'other', service: 'q' }),
      alert({ kind: 'exited', service: undefined }),
    ];
    expect(failingServices(alerts, 'web')).toEqual(['b', 'a']);
    expect(failingServices(undefined, 'web')).toEqual([]);
  });
});
