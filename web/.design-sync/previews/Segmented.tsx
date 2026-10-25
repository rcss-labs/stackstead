import { Segmented } from 'stackstead-ui';

export function TimeRange() {
  return (
    <Segmented
      label="Time range"
      value="1h"
      onChange={() => {}}
      options={[
        { value: '15m', label: '15m' },
        { value: '1h', label: '1h' },
        { value: '6h', label: '6h' },
        { value: '24h', label: '24h' },
      ]}
    />
  );
}

export function Small() {
  return (
    <Segmented
      label="Log stream"
      size="sm"
      value="all"
      onChange={() => {}}
      options={[
        { value: 'all', label: 'All' },
        { value: 'stdout', label: 'stdout' },
        { value: 'stderr', label: 'stderr' },
      ]}
    />
  );
}
