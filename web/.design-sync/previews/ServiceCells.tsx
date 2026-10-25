import { ServiceCells } from 'stackstead-ui';

const item = { display: 'flex', gap: 10, alignItems: 'center', fontSize: 12, color: 'var(--muted)' };

export function Mixed() {
  return (
    <div style={{ display: 'grid', gap: 10 }}>
      <span style={item}><ServiceCells count={4} good={4} bad={0} starting={0} title="4 of 4 running" /> 4 of 4 running</span>
      <span style={item}><ServiceCells count={6} good={3} bad={1} starting={1} title="3 running, 1 starting, 1 failed, 1 stopped" /> 3 running, 1 starting, 1 failed, 1 stopped</span>
      <span style={item}><ServiceCells count={3} good={0} bad={0} starting={0} title="Stopped" /> stopped</span>
    </div>
  );
}

export function Large() {
  return <ServiceCells count={12} good={9} bad={1} starting={2} title="9 running, 2 starting, 1 failed" />;
}
