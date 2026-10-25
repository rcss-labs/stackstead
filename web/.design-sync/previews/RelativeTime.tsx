import { PanelLabel, RelativeTime } from 'stackstead-ui';

const ago = (sec: number) => new Date(Date.now() - sec * 1000).toISOString();
const row = { display: 'flex', justifyContent: 'space-between', gap: 24, padding: '6px 0', borderBottom: '1px solid var(--border)' };

export function InList() {
  return (
    <div style={{ maxWidth: 320 }}>
      <div style={row}><PanelLabel as="span">Deployed</PanelLabel><RelativeTime iso={ago(42)} /></div>
      <div style={row}><PanelLabel as="span">Image pulled</PanelLabel><RelativeTime iso={ago(60 * 17)} /></div>
      <div style={row}><PanelLabel as="span">Created</PanelLabel><RelativeTime iso={ago(86400 * 3)} /></div>
    </div>
  );
}

export function Ranges() {
  return (
    <div style={{ display: 'flex', gap: 16 }}>
      <RelativeTime iso={ago(1)} />
      <RelativeTime iso={ago(30)} />
      <RelativeTime iso={ago(60 * 5)} />
      <RelativeTime iso={ago(3600 * 4)} />
      <RelativeTime iso={ago(86400 * 9)} />
    </div>
  );
}
