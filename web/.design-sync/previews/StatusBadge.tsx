import { StatusBadge } from 'stackstead-ui';

const row = { display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' as const };

export function Tones() {
  return (
    <div style={row}>
      <StatusBadge status="running" />
      <StatusBadge status="starting" />
      <StatusBadge status="stopped" />
      <StatusBadge status="unhealthy" />
    </div>
  );
}

export function AllStatuses() {
  const all = ['running', 'healthy', 'succeeded', 'partial', 'timed_out', 'restarting', 'paused', 'exited', 'created', 'cancelled', 'failed', 'dead', 'removing'];
  return (
    <div style={row}>
      {all.map((s) => (
        <StatusBadge key={s} status={s} />
      ))}
    </div>
  );
}
