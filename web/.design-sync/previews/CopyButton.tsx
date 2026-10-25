import { CopyButton } from 'stackstead-ui';

const row = { display: 'flex', gap: 8, alignItems: 'center' };
const code = { fontFamily: 'var(--mono)', fontSize: 12, padding: '4px 8px', background: 'var(--raised)', border: '1px solid var(--border)', borderRadius: 4 };

export function Ghost() {
  return (
    <div style={row}>
      <code style={code}>docker compose -p monitoring up -d</code>
      <CopyButton text="docker compose -p monitoring up -d" />
    </div>
  );
}

export function Icon() {
  return (
    <div style={row}>
      <code style={code}>sha256:4f2a9c81e0b7</code>
      <CopyButton text="sha256:4f2a9c81e0b7" variant="icon" label="Copy image digest" />
    </div>
  );
}

export function Small() {
  return (
    <div style={row}>
      <code style={code}>sst_7f3a9c1e42b84d0d</code>
      <CopyButton text="sst_7f3a9c1e42b84d0d" label="Copy token" size="sm" />
    </div>
  );
}
