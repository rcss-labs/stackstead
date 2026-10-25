import { Spinner } from 'stackstead-ui';

export function Default() {
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center', color: 'var(--muted)' }}>
      <Spinner label="Loading containers" />
      <span>Loading containers…</span>
    </div>
  );
}
