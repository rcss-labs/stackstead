import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import type { BackupsApi } from '$lib/api/backups';
import type { BackupConfig } from '$lib/api/backupTypes';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { Segmented } from '../components/Segmented';
import { Pill, SettingsCard } from '../settings/SettingsCard';
import styles from './backups.module.css';

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

const formatNextRun = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const FREQUENCIES = [
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
];

/** The S3 secret is write-only: it is sent only when typed, and an untouched field keeps the stored one. */
export function BackupScheduleCard({ api, onRunStarted }: { api: BackupsApi; onRunStarted?: () => void }) {
  const uid = useId();
  const [config, setConfig] = useState<BackupConfig>();
  const [secret, setSecret] = useState('');
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<string>();

  const load = useCallback(() => {
    api.config().then(setConfig, (e: unknown) => setError(messageOf(e)));
  }, [api]);
  useEffect(load, [load]);

  if (!config) {
    return (
      <SettingsCard id="backup-schedule-h" title="Backups">
        {error ? (
          <Banner kind="danger" title="Backups">
            {error}
          </Banner>
        ) : (
          <span className={styles.muted}>Loading…</span>
        )}
      </SettingsCard>
    );
  }

  const { schedule, local, s3 } = config;
  const patch = (next: Partial<BackupConfig>) => setConfig({ ...config, ...next });
  const patchS3 = (next: Partial<BackupConfig['s3']>) => patch({ s3: { ...s3, ...next } });
  const withSecret = (): BackupConfig['s3'] => {
    const { secretKey: _drop, ...rest } = s3;
    return secret === '' ? rest : { ...rest, secretKey: secret };
  };

  async function save(event: FormEvent) {
    event.preventDefault();
    if (saving) return;
    setSaving(true);
    setError(undefined);
    try {
      setConfig(await api.saveConfig({ ...config!, s3: withSecret() }));
      setSecret('');
      toasts.push({ kind: 'success', message: 'Backup settings saved.' });
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setSaving(false);
    }
  }

  async function test() {
    setTesting(true);
    try {
      await api.testS3(withSecret());
      setResult('Connected: wrote and removed a test object.');
    } catch (e) {
      setResult(`Failed: ${messageOf(e)}`);
    } finally {
      setTesting(false);
    }
  }

  async function clearSecret() {
    try {
      await api.clearS3Secret();
      patchS3({ secretKeySet: false });
      setSecret('');
    } catch (e) {
      setError(messageOf(e));
    }
  }

  async function runNow() {
    setRunning(true);
    try {
      await api.run();
      toasts.push({ kind: 'info', message: 'Backup started.' });
      onRunStarted?.();
    } catch (e) {
      toasts.push({ kind: 'danger', message: messageOf(e) });
    } finally {
      setRunning(false);
    }
  }

  return (
    <SettingsCard
      id="backup-schedule-h"
      title="Backups"
      headerEnd={
        schedule.enabled && config.nextRun ? (
          <Pill tone="info">{`Next run ${formatNextRun(config.nextRun)}`}</Pill>
        ) : (
          <Pill tone="muted">Schedule off</Pill>
        )
      }
      footer="Archives hold this server's database, settings and stack files, plus the volumes and images you choose per stack."
    >
      {config.pausedAfterRestore && (
        <Banner kind="warning" title="Backups were paused after a restore. Check the destination, then turn the schedule back on." />
      )}
      {error && (
        <Banner kind="danger" title="Backups">
          {error}
        </Banner>
      )}
      <form className={styles.form} onSubmit={(e) => void save(e)}>
        <label className={styles.check}>
          <input
            type="checkbox"
            checked={schedule.enabled}
            onChange={(e) => patch({ schedule: { ...schedule, enabled: e.target.checked } })}
          />
          Enabled
        </label>
        <div className={styles.row}>
          <Segmented
            label="Frequency"
            options={FREQUENCIES}
            value={schedule.frequency}
            onChange={(value) => patch({ schedule: { ...schedule, frequency: value as 'daily' | 'weekly' } })}
          />
          {schedule.frequency === 'weekly' && (
            <Field label="Weekday" id={`${uid}-weekday`}>
              {({ readOnly: _, ...c }) => (
                <select {...c} value={schedule.weekday} onChange={(e) => patch({ schedule: { ...schedule, weekday: Number(e.target.value) } })}>
                  {WEEKDAYS.map((day, i) => (
                    <option key={day} value={i}>
                      {day}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          )}
          <Field label="Time" id={`${uid}-time`} hint="HH:MM, server time">
            {({ readOnly: _, ...c }) => (
              <input {...c} type="time" value={schedule.time} onChange={(e) => patch({ schedule: { ...schedule, time: e.target.value } })} />
            )}
          </Field>
        </div>

        <div className={styles.group}>
          <h3 className={styles.groupTitle}>Local folder</h3>
          <label className={styles.check}>
            <input type="checkbox" checked={local.enabled} onChange={(e) => patch({ local: { ...local, enabled: e.target.checked } })} />
            Save archives to a folder on this server
          </label>
          <div className={styles.grid}>
            <Field label="Path" id={`${uid}-path`} mono>
              {({ readOnly: _, ...c }) => <input {...c} value={local.path} onChange={(e) => patch({ local: { ...local, path: e.target.value } })} />}
            </Field>
            <Field label="Keep last" id={`${uid}-keep`} hint="1 to 1000 archives">
              {({ readOnly: _, ...c }) => (
                <input {...c} type="number" min={1} max={1000} value={local.keepLast} onChange={(e) => patch({ local: { ...local, keepLast: Number(e.target.value) } })} />
              )}
            </Field>
          </div>
        </div>

        <div className={styles.group}>
          <h3 className={styles.groupTitle}>S3</h3>
          <label className={styles.check}>
            <input type="checkbox" checked={s3.enabled} onChange={(e) => patchS3({ enabled: e.target.checked })} />
            Upload archives to an S3 bucket
          </label>
          <div className={styles.grid}>
            <Field label="Bucket" id={`${uid}-bucket`}>
              {({ readOnly: _, ...c }) => <input {...c} value={s3.bucket} onChange={(e) => patchS3({ bucket: e.target.value })} />}
            </Field>
            <Field label="Prefix" id={`${uid}-prefix`}>
              {({ readOnly: _, ...c }) => <input {...c} value={s3.prefix} onChange={(e) => patchS3({ prefix: e.target.value })} />}
            </Field>
            <Field label="Region" id={`${uid}-region`}>
              {({ readOnly: _, ...c }) => <input {...c} value={s3.region} onChange={(e) => patchS3({ region: e.target.value })} />}
            </Field>
            <Field label="Endpoint" id={`${uid}-endpoint`} hint="Leave empty for AWS">
              {({ readOnly: _, ...c }) => <input {...c} value={s3.endpoint} onChange={(e) => patchS3({ endpoint: e.target.value })} />}
            </Field>
            <Field label="Access key ID" id={`${uid}-akid`} mono>
              {({ readOnly: _, ...c }) => <input {...c} value={s3.accessKeyId} autoComplete="off" onChange={(e) => patchS3({ accessKeyId: e.target.value })} />}
            </Field>
            <Field label="Secret access key" id={`${uid}-secret`} hint="Saving with this empty keeps the stored secret.">
              {({ readOnly: _, ...c }) => <input {...c} type="password" value={secret} autoComplete="new-password" onChange={(e) => setSecret(e.target.value)} />}
            </Field>
          </div>
          <label className={styles.check}>
            <input type="checkbox" checked={s3.pathStyle} onChange={(e) => patchS3({ pathStyle: e.target.checked })} />
            Path-style addressing
          </label>
          <div className={styles.row}>
            {s3.secretKeySet ? <Pill tone="success">Secret set</Pill> : <Pill tone="muted">Secret not set</Pill>}
            {s3.secretKeySet && (
              <Button variant="ghost" size="sm" onClick={() => void clearSecret()}>
                Clear secret
              </Button>
            )}
          </div>
        </div>

        <div className={styles.actions}>
          <Button variant="secondary" loading={testing} onClick={() => void test()}>
            Test connection
          </Button>
          <Button variant="secondary" loading={running} onClick={() => void runNow()}>
            Back up now
          </Button>
          <Button variant="primary" type="submit" loading={saving}>
            Save
          </Button>
        </div>
      </form>
      {result !== undefined && (
        <span className={styles.result} role="status">
          {result}
        </span>
      )}
    </SettingsCard>
  );
}
