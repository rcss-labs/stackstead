import { Sparkline } from 'stackstead-ui';

const cpu = [4, 6, 5, 9, 14, 12, 18, 11, 8, 10, 15, 22, 19, 13, 9, 7];
const mem = [310, 312, 318, 330, 342, 351, 360, 372, 388, 395, 402, 408, 410, 412, 411, 412];
const item = { display: 'flex', gap: 8, alignItems: 'center', fontFamily: 'var(--mono)', fontSize: 12 };

export function Tones() {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {(['accent', 'info', 'success', 'warning', 'danger'] as const).map((tone, i) => (
        <span key={tone} style={item}>
          <Sparkline tone={tone} values={cpu.map((v, j) => v + ((j * (i + 3)) % 7))} /> {tone}
        </span>
      ))}
    </div>
  );
}

export function MetricRow() {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <span style={item}><Sparkline tone="accent" values={cpu} width={120} height={24} /> CPU 7.0%</span>
      <span style={item}><Sparkline tone="info" values={mem} width={120} height={24} /> MEM 412 MiB</span>
    </div>
  );
}

export function Gaps() {
  return (
    <span style={item}>
      <Sparkline tone="warning" values={[3, 5, null, null, 8, 6, 9, null, 4, 7]} width={120} height={24} /> samples missing
    </span>
  );
}
