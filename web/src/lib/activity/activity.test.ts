import { describe, expect, it, vi } from 'vitest';
import type { ActionEntry, DockerEvent, Operation } from '../api/types';
import type { BackupRun } from '../api/backupTypes';
import {
  fromActions,
  fromBackupRuns,
  fromEvents,
  fromOperations,
  mergeActivity,
  mergeEvents,
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
    expect(readLastVisit('a')).toBeNull();
    expect(() => writeLastVisit(5, 'a')).not.toThrow();
    get.mockRestore();
    set.mockRestore();
  });
  it('round-trips', () => {
    writeLastVisit(1234, 'a');
    expect(readLastVisit('a')).toBe(1234);
    localStorage.clear();
  });
  it('is kept per server', () => {
    writeLastVisit(1234, 'a');
    expect(readLastVisit('b')).toBeNull();
    writeLastVisit(99, 'b');
    expect(readLastVisit('a')).toBe(1234);
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

describe('action result tone', () => {
  const act = (result: string, over: Partial<ActionEntry> = {}): ActionEntry => ({ id: 1, ts: '2026-11-07T10:00:00Z', username: 'a', kind: 'storage.prune', scope: 'host', target: '', detail: '', bytes: null, result, operationId: '', ...over });
  it.each([
    ['ok', 'success'],
    ['success', 'success'],
    ['succeeded', 'success'],
    ['deleted', 'success'],
    ['fixed', 'success'],
    ['exit 0', 'success'],
    ['failed', 'danger'],
    ['error', 'danger'],
    ['exit 1', 'danger'],
    ['exit 127', 'danger'],
    ['partial', 'warning'],
    ['skipped', 'warning'],
    ['started', 'muted'],
    ['idle', 'muted'],
    ['', 'muted'],
  ])('%s reads as %s', (result, tone) => {
    expect(fromActions([act(result)])[0].tone).toBe(tone);
  });
});

describe('event tone', () => {
  const ev = (action: string, over: Partial<DockerEvent> = {}): DockerEvent => ({ action, container: 'web-1', project: 'web', service: 'web', image: 'x', time: '2026-11-07T10:00:00Z', ...over });
  it('die is danger only with a non-zero exit code', () => {
    expect(fromEvents([ev('die', { exitCode: 0 })])[0].tone).not.toBe('danger');
    expect(fromEvents([ev('die', { exitCode: 137 })])[0].tone).toBe('danger');
  });
  it('kill, which a routine stop also emits, is not danger', () => {
    expect(fromEvents([ev('kill')])[0].tone).not.toBe('danger');
  });
});

describe('image links', () => {
  it('image operations link to the image, not a stack', () => {
    const [i] = fromOperations([op({ stack: 'image/acme/web-app', action: 'pull' })]);
    expect(i.path).toBe('/images/web-app');
    expect(i.title).toBe('pull image web-app');
  });
  it('image audit actions link to the image by slug', () => {
    const a: ActionEntry = { id: 1, ts: '2026-11-07T10:00:00Z', username: 'a', kind: 'image.update', scope: 'web-app', target: 'x', detail: '', bytes: null, result: 'succeeded', operationId: '' };
    expect(fromActions([a])[0].path).toBe('/images/web-app');
  });
});

describe('mergeEvents', () => {
  const e = (time: string, action = 'start'): DockerEvent => ({ action, container: 'c', project: '', service: '', image: 'i', time });
  it('joins fetched and live events once each, newest first', () => {
    const live = [e('2026-11-07T10:02:00Z'), e('2026-11-07T10:01:00Z')];
    const recent = [e('2026-11-07T10:01:00Z'), e('2026-11-07T10:00:00Z')];
    expect(mergeEvents(live, recent).map((x) => x.time)).toEqual(['2026-11-07T10:02:00Z', '2026-11-07T10:01:00Z', '2026-11-07T10:00:00Z']);
  });
  it('keeps distinct actions at the same instant', () => {
    expect(mergeEvents([e('2026-11-07T10:00:00Z', 'die')], [e('2026-11-07T10:00:00Z', 'stop')])).toHaveLength(2);
  });
});
