import { Button, Dialog } from 'stackstead-ui';

export function RenameStack() {
  return (
    <Dialog open title="Rename stack" onClose={() => {}}>
      <p style={{ margin: '8px 0 16px', color: 'var(--muted)' }}>
        Containers keep running. The new name is used for the compose project and its networks.
      </p>
      <input defaultValue="monitoring" aria-label="Stack name" style={{ width: '100%', padding: '6px 8px', border: '1px solid var(--border-input)', borderRadius: 4, background: 'var(--surface)' }} />
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
        <Button variant="secondary">Cancel</Button>
        <Button variant="primary">Rename</Button>
      </div>
    </Dialog>
  );
}
