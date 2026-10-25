import { Led } from 'stackstead-ui';

const tones = ['success', 'warning', 'danger', 'info', 'muted', 'faint'] as const;
const item = { display: 'flex', gap: 6, alignItems: 'center', fontFamily: 'var(--mono)', fontSize: 12 };

export function Tones() {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {tones.map((tone) => (
        <span key={tone} style={item}>
          <Led tone={tone} label={tone} /> {tone}
        </span>
      ))}
    </div>
  );
}

export function Sizes() {
  return (
    <div style={{ display: 'flex', gap: 16, alignItems: 'center' }}>
      {([7, 8, 10] as const).map((size) => (
        <span key={size} style={item}>
          <Led tone="success" size={size} /> {size}px
        </span>
      ))}
    </div>
  );
}

export function Pulse() {
  return (
    <span style={item}>
      <Led tone="warning" pulse label="Starting" /> starting
    </span>
  );
}
