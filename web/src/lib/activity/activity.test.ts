import { describe, expect, it, vi } from 'vitest';
import type { ActionEntry, DockerEvent, Operation } from '../api/types';
import type { BackupRun } from '../api/backupTypes';
import {
  fromActions,
  fromBackupRuns,
  fromEvents,
  fromOperations,
  mergeActivity,
  readLastVisit,
  reachNote,
  sinceLastVisit,
  writeLastVisit,
  type ActivityItem,
} from './activity';

const item = (id: string, time: string, kind: ActivityItem['kind'] = 'event'): ActivityItem => ({
  id,
  kind,
  time,
  title: id,
  detail: '',
  tone: 'muted',
  path: '/',
});

const op = (over: Partial<Operation> = {}): Operation => ({
  id: 'op1',
  stack: 'web',
  action: 'up',
  state: 'succeeded',
  startedAt: '2026-11-07T10:00:00Z',
  ...over,
});

describe('mergeActivity', () => {
  it('merge sorts newest first across all sources', () => {
    const merged = mergeActivity([
      [item('a', '2026-11-07T10:00:00Z'), item('c', '2026-11-07T08:00:00Z')],
      [item('b', '2026-11-07T09:00:00Z', 'operation'), item('d', 'not a date')],
    ]);
    expect(merged.map((i) => i.id)).toEqual(['a', 'b', 'c', 'd']);
  });
});

describe('deep links', () => {
  it('each kind links to the page that shows it', () => {
    const event: DockerEvent = { action: 'die', container: 'web-1', project: 'web', service: 'web', image: 'x', time: '2026-11-07T10:00:00Z', exitCode: 1 };
    const action: ActionEntry = { id: 1, ts: '2026-11-07T10:00:00Z', username: 'a', kind: 'storage.prune', scope: 'host', target: '', detail: '', bytes: null, result: 'ok', operationId: '' };
    const run = { id: 3, startedAt: '2026-11-07T10:00:00Z', trigger: 'schedule', status: 'success', size: 1, destinations: [], items: [] } as BackupRun;
    expect(fromEvents([event])[0].path).toBe('/containers/web-1');
    expect(fromOperations([op()])[0].path).toBe('/stacks/web');
    expect(fromActions([action])[0].path).toBe('/storage');
    expect(fromBackupRuns([run])[0].path).toBe('/backups');
  });

  it('failed operations and backups read as danger', () => {
    expect(fromOperations([op({ state: 'failed' })])[0].tone).toBe('danger');
    expect(fromBackupRuns([{ id: 1, startedAt: '2026-11-07T10:00:00Z', trigger: 'manual', status: 'failed', size: 0, destinations: [], items: [], error: 'boom' }])[0].tone).toBe('danger');
  });
});

describe('sinceLastVisit', () => {
  const items = [item('a', '2026-11-07T10:00:00Z', 'operation'), item('b', '2026-11-07T08:00:00Z', 'event'), item('c', '2026-11-06T08:00:00Z', 'event')];
  it('summary is omitted on first visit', () => {
    expect(sinceLastVisit(items, null)).toBe('');
  });
  it('counts what is newer than the last visit by kind', () => {
    expect(sinceLastVisit(items, Date.parse('2026-11-07T07:00:00Z'))).toBe('1 operation and 1 Docker event since your last visit.');
  });
  it('says so when nothing is new', () => {
    expect(sinceLastVisit(items, Date.parse('2026-11-08T00:00:00Z'))).toBe('Nothing new since your last visit.');
  });
});

describe('last visit storage', () => {
  it('is stored safely when storage throws', () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    expect(readLastVisit()).toBeNull();
    expect(() => writeLastVisit(5)).not.toThrow();
    get.mockRestore();
    set.mockRestore();
  });
  it('round-trips', () => {
    writeLastVisit(1234);
    expect(readLastVisit()).toBe(1234);
    localStorage.clear();
  });
});

describe('reachNote', () => {
  const now = new Date('2026-11-07T12:00:00Z');
  it('states the limit and the oldest entry for each kind', () => {
    expect(reachNote('event', [item('a', '2026-11-07T10:00:00Z')], now)).toContain('last 50');
    expect(reachNote('event', [item('a', '2026-11-07T10:00:00Z')], now)).toContain('2h ago');
    expect(reachNote('operation', [], now)).toContain('one hour');
    expect(reachNote('operation', [], now)).toContain('none right now');
    expect(reachNote('action', [], now)).toContain('newest 100');
    expect(reachNote('backup', [], now)).toContain('newest 50');
  });
});
