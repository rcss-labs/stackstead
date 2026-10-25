import { Chip } from 'stackstead-ui';

const row = { display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' as const };

export function FilterRow() {
  return (
    <div style={row}>
      <Chip pressed onClick={() => {}} count={14}>All</Chip>
      <Chip pressed={false} onClick={() => {}} count={11}>Running</Chip>
      <Chip pressed={false} onClick={() => {}} count={2}>Stopped</Chip>
      <Chip pressed={false} onClick={() => {}} count={1} tone="danger">Unhealthy</Chip>
    </div>
  );
}

export function Pressed() {
  return (
    <div style={row}>
      <Chip pressed onClick={() => {}}>Running</Chip>
      <Chip pressed={false} onClick={() => {}}>Running</Chip>
    </div>
  );
}

export function DangerPressed() {
  return (
    <div style={row}>
      <Chip pressed onClick={() => {}} count={3} tone="danger">Failed</Chip>
      <Chip pressed={false} onClick={() => {}} count={3} tone="danger">Failed</Chip>
    </div>
  );
}
