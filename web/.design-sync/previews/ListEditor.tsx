import { ListEditor } from 'stackstead-ui';

const input = { width: '100%', fontFamily: 'var(--mono)', padding: '4px 8px', border: '1px solid var(--border-input)', borderRadius: 4, background: 'var(--surface)' };

export function PortMappings() {
  return (
    <div style={{ maxWidth: 420 }}>
      <ListEditor
        title="Ports"
        hint="host:container"
        addLabel="Add port mapping"
        items={['8080:80', '8443:443', '9090:9090']}
        onChange={() => {}}
        newItem={() => ''}
        row={(item, i, update, errorId) => (
          <input style={input} aria-label={`Port mapping ${i + 1}`} aria-describedby={errorId} value={item} onChange={(e) => update(e.target.value)} />
        )}
      />
    </div>
  );
}

export function WithError() {
  return (
    <div style={{ maxWidth: 420 }}>
      <ListEditor
        title="Environment"
        addLabel="Add variable"
        items={['TZ=Europe/Berlin', 'LOG_LEVEL']}
        errors={{ 1: 'Use NAME=value' }}
        onChange={() => {}}
        newItem={() => ''}
        row={(item, i, update, errorId) => (
          <input style={input} aria-label={`Variable ${i + 1}`} aria-describedby={errorId} aria-invalid={errorId ? 'true' : undefined} value={item} onChange={(e) => update(e.target.value)} />
        )}
      />
    </div>
  );
}

export function Readonly() {
  return (
    <div style={{ maxWidth: 420 }}>
      <ListEditor
        title="Volumes"
        readonly
        addLabel="Add volume"
        items={['grafana-data:/var/lib/grafana', './provisioning:/etc/grafana/provisioning']}
        onChange={() => {}}
        newItem={() => ''}
        row={(item) => <span style={{ fontFamily: 'var(--mono)', fontSize: 12 }}>{item}</span>}
      />
    </div>
  );
}
