import type { HealthHistory, HealthRun, HealthState } from '$lib/api/types';

export const TIMELINE_SPAN_SECONDS = 3600;
/** Value of the `span` query parameter for the health endpoint. */
export const TIMELINE_SPAN = '60m';
export const TIMELINE_AXIS: string[] = ['−60m', '−30m', 'now'];

export type TimelineTone = 'success' | 'warning' | 'danger' | 'down' | 'nodata';

export const TIMELINE_LEGEND: { tone: TimelineTone; label: string }[] = [
  { tone: 'success', label: 'healthy' },
  { tone: 'warning', label: 'restart' },
  { tone: 'danger', label: 'unhealthy' },
  { tone: 'down', label: 'down' },
];

export interface TimelineSegment {
  state: HealthState;
  tone: TimelineTone;
  from: number;
  to: number;
  /** Seconds covered; used as the CSS `flex` value. */
  weight: number;
  title: string;
  pulse: boolean;
}

export interface TimelineLane {
  service: string;
  restarts: number;
  segments: TimelineSegment[];
}

export function timelineTone(state: HealthState): TimelineTone {
  switch (state) {
    case 'running':
    case 'healthy':
      return 'success';
    case 'starting':
    case 'restarting':
      return 'warning';
    case 'unhealthy':
      return 'danger';
    case 'exited':
    case 'removed':
      return 'down';
    case 'nodata':
      return 'nodata';
  }
}

/** "5 min": whole minutes, never below one so a short run still reads as present. */
export function minutesText(seconds: number): string {
  return `${Math.max(1, Math.round(seconds / 60))} min`;
}

export function stateLabel(state: HealthState): string {
  return state === 'nodata' ? 'no data' : state;
}

export function restartsText(restarts: number): string {
  return restarts === 1 ? 'restarted once' : `restarted ${restarts} times`;
}

export function segmentTitle(state: HealthState, seconds: number): string {
  return `${stateLabel(state)} · ${minutesText(seconds)}`;
}

/**
 * "db: healthy 55 min, unhealthy 5 min, restarted 3 times": total time per state (in order of first
 * appearance) and the restart count, so a crash-looping lane stays one short sentence.
 */
export function laneSummary(lane: TimelineLane): string {
  const totals = new Map<HealthState, number>();
  for (const s of lane.segments) totals.set(s.state, (totals.get(s.state) ?? 0) + s.weight);
  const parts = Array.from(totals, ([state, seconds]) => `${stateLabel(state)} ${minutesText(seconds)}`);
  if (lane.restarts > 0) parts.push(restartsText(lane.restarts));
  return `${lane.service}: ${parts.join(', ')}`;
}

function segment(state: HealthState, from: number, to: number): TimelineSegment {
  return { state, tone: timelineTone(state), from, to, weight: to - from, title: segmentTitle(state, to - from), pulse: false };
}

function laneSegments(runs: HealthRun[], historyFrom: number, windowStart: number, now: number): TimelineSegment[] {
  const clipped = runs
    .map((run) => ({ state: run.state, from: Math.max(run.from, windowStart), to: Math.min(run.to, now) }))
    .filter((run) => run.to > run.from);
  if (clipped.length === 0) return [segment('nodata', windowStart, now)];

  const first = clipped[0]!;
  const last = clipped[clipped.length - 1]!;
  last.to = now;
  const segments: TimelineSegment[] = [];
  if (first.from > windowStart) {
    // Before the response's own start there is no data; otherwise the first run simply reaches back.
    if (historyFrom > windowStart) segments.push(segment('nodata', windowStart, first.from));
    else first.from = windowStart;
  }
  for (const run of clipped) segments.push(segment(run.state, run.from, run.to));

  // Equal neighbours (e.g. a leading gap and a nodata run) read as one stretch.
  const merged: TimelineSegment[] = [];
  for (const seg of segments) {
    const prev = merged[merged.length - 1];
    if (prev && prev.state === seg.state) merged[merged.length - 1] = segment(seg.state, prev.from, seg.to);
    else merged.push(seg);
  }
  const tail = merged[merged.length - 1]!;
  tail.pulse = tail.tone === 'danger';
  return merged;
}

export function timelineLanes(history: HealthHistory | undefined, services: string[], nowSeconds: number): TimelineLane[] {
  const windowStart = nowSeconds - TIMELINE_SPAN_SECONDS;
  return services.map((service) => {
    const health = history?.services.find((s) => s.service === service);
    if (!history || !health) return { service, restarts: 0, segments: [segment('nodata', windowStart, nowSeconds)] };
    return { service, restarts: health.restarts, segments: laneSegments(health.runs, history.from, windowStart, nowSeconds) };
  });
}
