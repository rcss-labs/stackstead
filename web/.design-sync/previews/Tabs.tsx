import { Tabs } from 'stackstead-ui';

const panel = { padding: '12px 0', color: 'var(--muted)' };

export function StackDetail() {
  return (
    <div style={{ maxWidth: 560 }}>
      <Tabs
        ariaLabel="Stack sections"
        selected="services"
        onSelect={() => {}}
        tabs={[
          { id: 'services', label: 'Services', badge: 4 },
          { id: 'logs', label: 'Logs' },
          { id: 'compose', label: 'Compose' },
          { id: 'history', label: 'History', badge: 12 },
        ]}
      >
        {(selected) => <div style={panel}>{selected === 'services' ? 'api, worker, postgres and redis are running.' : selected}</div>}
      </Tabs>
    </div>
  );
}

export function NoBadges() {
  return (
    <div style={{ maxWidth: 560 }}>
      <Tabs
        ariaLabel="Settings sections"
        selected="github"
        onSelect={() => {}}
        tabs={[
          { id: 'general', label: 'General' },
          { id: 'github', label: 'GitHub' },
          { id: 'images', label: 'Images' },
        ]}
      >
        {() => <div style={panel}>Connect a GitHub token to deploy stacks from private repositories.</div>}
      </Tabs>
    </div>
  );
}
