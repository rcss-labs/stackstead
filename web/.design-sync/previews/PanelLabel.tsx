import { PanelLabel } from 'stackstead-ui';

export function Tones() {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <PanelLabel>Services</PanelLabel>
      <PanelLabel tone="faint">Last deployed</PanelLabel>
      <PanelLabel tone="success">Healthy</PanelLabel>
      <PanelLabel tone="warning">Degraded</PanelLabel>
      <PanelLabel tone="danger">Errors</PanelLabel>
      <PanelLabel tone="info">Info</PanelLabel>
    </div>
  );
}

export function SectionHeader() {
  return (
    <section style={{ maxWidth: 420, border: '1px solid var(--border)', borderRadius: 6, background: 'var(--surface)', padding: 12 }}>
      <PanelLabel as="h2">Resource usage</PanelLabel>
      <p style={{ marginTop: 8, color: 'var(--muted)' }}>CPU 12.4% · Memory 412 MiB of 2 GiB</p>
    </section>
  );
}
