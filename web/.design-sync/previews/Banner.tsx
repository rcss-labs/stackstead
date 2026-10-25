import { Banner } from 'stackstead-ui';

const stack = { display: 'grid', gap: 12, maxWidth: 560 };

export function Kinds() {
  return (
    <div style={stack}>
      <Banner kind="info" title="Update available">Stackstead 0.9.2 is ready. Restart the agent to apply it.</Banner>
      <Banner kind="warning" title="Disk usage at 87%">Prune unused images to free space on /var/lib/docker.</Banner>
      <Banner kind="danger" title="Deploy failed">Service api exited with code 1 during startup.</Banner>
    </div>
  );
}

export function Dismissible() {
  return (
    <div style={stack}>
      <Banner kind="info" title="Metrics are sampled every 10 seconds" dismissible />
    </div>
  );
}

export function TitleOnly() {
  return (
    <div style={stack}>
      <Banner kind="info" title="This stack is managed outside Stackstead" />
    </div>
  );
}
