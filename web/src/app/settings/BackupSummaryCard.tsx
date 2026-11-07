import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { createBackupsApi } from '$lib/api/backups';
import type { BackupConfig } from '$lib/api/backupTypes';
import { Banner } from '../components/Banner';
import { PanelLabel } from '../components/PanelLabel';
import { Spinner } from '../components/Spinner';
import { Pill, SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';

const api = createBackupsApi('');
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

function scheduleText({ schedule }: BackupConfig): string {
  if (!schedule.enabled) return 'Off';
  return schedule.frequency === 'weekly'
    ? `Weekly on ${WEEKDAYS[schedule.weekday] ?? 'Sunday'} at ${schedule.time}`
    : `Daily at ${schedule.time}`;
}

/** Destinations that are switched on; secrets and keys are never read here. */
function destinations({ local, s3 }: BackupConfig): string[] {
  return [
    ...(local.enabled ? [local.path] : []),
    ...(s3.enabled ? [`s3://${s3.bucket}${s3.prefix ? `/${s3.prefix.replace(/^\/+/, '')}` : ''}`] : []),
  ];
}

/** Read-only: the schedule has one editing home, the Schedule tab on the Backups screen. */
export function BackupSummaryCard() {
  const [config, setConfig] = useState<BackupConfig>();
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let current = true;
    api.config().then(
      (loaded) => current && setConfig(loaded),
      () => current && setFailed(true),
    );
    return () => {
      current = false;
    };
  }, []);

  const where = config ? destinations(config) : [];

  return (
    <SettingsCard id="backup-summary-h" title="Backups" footer="The schedule, destinations and encryption are edited in one place: the Schedule tab on the Backups screen.">
      {failed && (
        <Banner kind="warning" title="Could not load the backup settings">
          Open the backup schedule to see them.
        </Banner>
      )}
      {!config && !failed && <Spinner />}
      {config && (
        <dl className={styles.status}>
          <div>
            <dt>
              <PanelLabel as="span">Schedule</PanelLabel>
            </dt>
            <dd className={styles.value}>{scheduleText(config)}</dd>
          </div>
          <div>
            <dt>
              <PanelLabel as="span">Destination</PanelLabel>
            </dt>
            <dd className={styles.value}>{where.length > 0 ? where.join(', ') : 'None enabled'}</dd>
          </div>
          <div>
            <dt>
              <PanelLabel as="span">Encryption</PanelLabel>
            </dt>
            <dd className={styles.value}>
              <Pill tone={config.encryption.enabled ? 'success' : 'muted'}>{config.encryption.enabled ? 'On' : 'Off'}</Pill>
            </dd>
          </div>
        </dl>
      )}
      <p>
        <Link to="/backups?tab=schedule">Open backup schedule</Link>
      </p>
    </SettingsCard>
  );
}
