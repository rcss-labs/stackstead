import { Button } from 'stackstead-ui';

const row = { display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' as const };

export function Variants() {
  return (
    <div style={row}>
      <Button variant="primary">Deploy stack</Button>
      <Button variant="secondary">View logs</Button>
      <Button variant="ghost">Edit compose</Button>
      <Button variant="danger">Remove</Button>
    </div>
  );
}

export function Sizes() {
  return (
    <div style={row}>
      <Button variant="secondary" size="md">Restart</Button>
      <Button variant="secondary" size="sm">Restart</Button>
      <Button variant="secondary" size="xs">Restart</Button>
    </div>
  );
}

export function States() {
  return (
    <div style={row}>
      <Button variant="primary" loading>Pulling images</Button>
      <Button variant="primary" disabledReason="Enter a token">Save token</Button>
      <Button variant="secondary" disabled>Stop</Button>
    </div>
  );
}

export function ConfirmDanger() {
  return (
    <div style={row}>
      <Button variant="secondary">Cancel</Button>
      <Button variant="danger" confirm>Delete volume</Button>
    </div>
  );
}
