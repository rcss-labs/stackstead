import { useStore } from '../hooks/useStore';
import { live, refreshStatus } from '$lib/stores/live';
import { Button } from '../components/Button';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';

export function SystemStatusCard() {
  const liveState = useStore(live);

  // Only an explicit 'unavailable' means down; 'unknown' (no answer yet) stays neutral.
  const dockerText =
    liveState.docker === 'ok' ? (liveState.dockerVersion ? `${liveState.dockerVersion} · ok` : 'ok') : liveState.docker;
  const dockerTone = liveState.docker === 'ok' ? 'success' : liveState.docker === 'unavailable' ? 'danger' : 'muted';
  const composeTone = liveState.compose === 'unknown' || liveState.compose === 'unavailable' ? 'muted' : 'success';
  const eventsTone = liveState.events === 'connected' ? 'success' : liveState.events === 'disconnected' ? 'danger' : 'muted';
  const metricsText = liveState.metricsDegraded
    ? 'degraded'
    : liveState.metricsIntervalSeconds
      ? `ok · ${liveState.metricsIntervalSeconds} s`
      : 'ok';
  const metricsTone = liveState.metricsDegraded ? 'warning' : 'success';

  const rows = [
    { label: 'Docker', testId: 'status-docker', tone: dockerTone, text: dockerText },
    { label: 'Compose', testId: 'status-compose', tone: composeTone, text: liveState.compose },
    { label: 'Events', testId: 'status-events', tone: eventsTone, text: liveState.events },
    { label: 'Metrics', testId: 'status-metrics', tone: metricsTone, text: metricsText },
  ] as const;

  return (
    <SettingsCard
      id="status-h"
      title="System status"
      headerEnd={
        <Button variant="secondary" size="sm" onClick={() => void refreshStatus()}>
          Refresh
        </Button>
      }
    >
      <dl className={styles.status}>
        {rows.map((row) => (
          <div key={row.label}>
            <dt>
              <PanelLabel as="span">{row.label}</PanelLabel>
            </dt>
            <dd className={styles.value} data-testid={row.testId}>
              <Led tone={row.tone} size={7} />
              {row.text}
            </dd>
          </div>
        ))}
      </dl>
    </SettingsCard>
  );
}
