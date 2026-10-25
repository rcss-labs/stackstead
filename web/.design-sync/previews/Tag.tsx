import { Tag } from 'stackstead-ui';

const row = { display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' as const };

export function Tones() {
  return (
    <div style={row}>
      <Tag>postgres:16.4</Tag>
      <Tag tone="info">compose</Tag>
      <Tag tone="success">latest</Tag>
      <Tag tone="warning">dangling</Tag>
      <Tag tone="danger">exit 137</Tag>
    </div>
  );
}

export function SansText() {
  return (
    <div style={row}>
      <Tag mono={false}>Git repository</Tag>
      <Tag mono={false} tone="info">Managed</Tag>
    </div>
  );
}
