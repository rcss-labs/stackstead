import { describe, expect, it } from 'vitest';
import type { HealthHistory, HealthState } from '$lib/api/types';
import { TIMELINE_AXIS, TIMELINE_LEGEND, TIMELINE_SPAN, TIMELINE_SPAN_SECONDS, segmentTitle, timelineLanes, timelineTone } from './timeline';

const now = 100_000;
const start = now - TIMELINE_SPAN_SECONDS;

function history(services: HealthHistory['services'], from = start, to = now): HealthHistory {
  return { from, to, services };
}

describe('timeline', () => {
  it('exposes the span constants', () => {
    expect(TIMELINE_SPAN_SECONDS).toBe(3600);
    expect(TIMELINE_SPAN).toBe('60m');
    expect(TIMELINE_AXIS).toEqual(['−60m', '−30m', 'now']);
  });

  it('maps every HealthState to its tone', () => {
    const table: [HealthState, string][] = [
      ['running', 'success'],
      ['healthy', 'success'],
      ['starting', 'warning'],
      ['restarting', 'warning'],
      ['unhealthy', 'danger'],
      ['exited', 'down'],
      ['removed', 'down'],
      ['nodata', 'nodata'],
    ];
    for (const [state, tone] of table) expect(timelineTone(state)).toBe(tone);
  });

  it('segment titles round to minutes with a 1 min floor and word nodata as no data', () => {
    expect(segmentTitle('unhealthy', 300)).toBe('unhealthy · 5 min');
    expect(segmentTitle('healthy', 20)).toBe('healthy · 1 min');
    expect(segmentTitle('nodata', 3600)).toBe('no data · 60 min');
  });

  it('legend lists healthy, restart, unhealthy, down in order', () => {
    expect(TIMELINE_LEGEND).toEqual([
      { tone: 'success', label: 'healthy' },
      { tone: 'warning', label: 'restart' },
      { tone: 'danger', label: 'unhealthy' },
      { tone: 'down', label: 'down' },
    ]);
  });

  it('lanes follow the services order and cover the window exactly', () => {
    const h = history([
      { service: 'db', restarts: 0, runs: [{ state: 'healthy', from: start, to: now }] },
      {
        service: 'app',
        restarts: 0,
        runs: [
          { state: 'starting', from: start, to: start + 600 },
          { state: 'healthy', from: start + 600, to: now },
        ],
      },
    ]);
    const lanes = timelineLanes(h, ['app', 'db'], now);
    expect(lanes.map((l) => l.service)).toEqual(['app', 'db']);
    for (const lane of lanes) {
      expect(lane.segments.reduce((sum, s) => sum + s.weight, 0)).toBe(3600);
      expect(lane.segments[0]!.from).toBe(start);
      expect(lane.segments.at(-1)!.to).toBe(now);
    }
    const app = lanes[0]!.segments;
    expect(app.map((s) => [s.state, s.tone, s.weight, s.title])).toEqual([
      ['starting', 'warning', 600, 'starting · 10 min'],
      ['healthy', 'success', 3000, 'healthy · 50 min'],
    ]);
  });

  it('extends the last run to now and clips runs outside the window', () => {
    const h = history(
      [
        {
          service: 'app',
          restarts: 0,
          runs: [
            { state: 'exited', from: start - 5000, to: start - 100 },
            { state: 'unhealthy', from: start - 50, to: start + 1000 },
            { state: 'healthy', from: start + 1000, to: now - 90 },
          ],
        },
      ],
      start - 5000,
      now - 90,
    );
    const segs = timelineLanes(h, ['app'], now)[0]!.segments;
    expect(segs.map((s) => s.state)).toEqual(['unhealthy', 'healthy']);
    expect(segs[0]!.from).toBe(start);
    expect(segs[0]!.weight).toBe(1000);
    expect(segs[1]!.to).toBe(now);
    expect(segs[1]!.weight).toBe(2600);
  });

  it('marks the part before a late response start as nodata', () => {
    const h = history([{ service: 'app', restarts: 0, runs: [{ state: 'healthy', from: start + 1200, to: now }] }], start + 1200, now);
    const segs = timelineLanes(h, ['app'], now)[0]!.segments;
    expect(segs.map((s) => [s.state, s.weight])).toEqual([
      ['nodata', 1200],
      ['healthy', 2400],
    ]);
  });

  it('merges equal adjacent states into one segment', () => {
    const h = history([{ service: 'app', restarts: 0, runs: [{ state: 'nodata', from: start + 1200, to: start + 2000 }, { state: 'healthy', from: start + 2000, to: now }] }], start + 1200, now);
    const segs = timelineLanes(h, ['app'], now)[0]!.segments;
    expect(segs.map((s) => [s.state, s.from, s.to, s.weight, s.title])).toEqual([
      ['nodata', start, start + 2000, 2000, 'no data · 33 min'],
      ['healthy', start + 2000, now, 1600, 'healthy · 27 min'],
    ]);
  });

  it('an omitted service and a missing history are one nodata segment', () => {
    const h = history([{ service: 'db', restarts: 2, runs: [{ state: 'healthy', from: start, to: now }] }]);
    const omitted = timelineLanes(h, ['app'], now)[0]!;
    expect(omitted.restarts).toBe(0);
    expect(omitted.segments).toHaveLength(1);
    expect(omitted.segments[0]).toMatchObject({ state: 'nodata', tone: 'nodata', from: start, to: now, weight: 3600, pulse: false });
    const missing = timelineLanes(undefined, ['app', 'db'], now);
    expect(missing).toHaveLength(2);
    expect(missing.every((l) => l.restarts === 0 && l.segments.length === 1 && l.segments[0]!.tone === 'nodata')).toBe(true);
  });

  it('only a trailing danger run pulses', () => {
    const runs = (a: HealthState, b: HealthState) => [
      { state: a, from: start, to: start + 1800 },
      { state: b, from: start + 1800, to: now },
    ];
    const bad = timelineLanes(history([{ service: 'app', restarts: 0, runs: runs('unhealthy', 'healthy') }]), ['app'], now)[0]!;
    expect(bad.segments.map((s) => s.pulse)).toEqual([false, false]);
    const worse = timelineLanes(history([{ service: 'app', restarts: 0, runs: runs('healthy', 'unhealthy') }]), ['app'], now)[0]!;
    expect(worse.segments.map((s) => s.pulse)).toEqual([false, true]);
  });

  it('restarts come from the response', () => {
    const h = history([{ service: 'app', restarts: 3, runs: [{ state: 'healthy', from: start, to: now }] }]);
    expect(timelineLanes(h, ['app'], now)[0]!.restarts).toBe(3);
  });
});
