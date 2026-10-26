import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { api, ApiError } from '$lib/api/client';
import { setupRestore } from '$lib/api/setupRestore';
import type { HostRestoreS3, HostRestoreSource, HostRestoreSummary } from '$lib/api/backupTypes';
import { openSSE, type SSEHandle, type SSEMessage } from '$lib/sse/client';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { Tabs } from '../components/Tabs';
import { AuthError, AuthHint } from './AuthCard';
import styles from './RestoreFromBackup.module.css';

type SourceTab = 's3' | 'path' | 'upload';
const SOURCE_TABS = [
  { id: 's3', label: 'S3' },
  { id: 'path', label: 'Path' },
  { id: 'upload', label: 'Upload' },
];

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Accepts `s3://bucket/key` or a bucket in the first field and a key in the second. */
function splitBucketAndKey(value: string): { bucket: string; key: string } {
  const trimmed = value.trim().replace(/^s3:\/\//, '');
  const slash = trimmed.indexOf('/');
  return slash < 0 ? { bucket: trimmed, key: '' } : { bucket: trimmed.slice(0, slash), key: trimmed.slice(slash + 1) };
}

const formatBytes = (n: number) => (n < 1024 * 1024 ? `${Math.ceil(n / 1024)} KiB` : `${(n / 1024 / 1024).toFixed(1)} MiB`);

type Stage = 'source' | 'inspected' | 'running' | 'failed';

/**
 * Restore this host from a whole-host backup before any admin exists. Secrets (S3 secret key, setup token) live in
 * state only until the request that needs them is sent.
 */
export function RestoreFromBackup({ pollMs = 2000, deadlineMs = 5 * 60_000 }: { pollMs?: number; deadlineMs?: number }) {
  const navigate = useNavigate();
  const [tab, setTab] = useState<SourceTab>('s3');
  const [stage, setStage] = useState<Stage>('source');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [setupToken, setSetupToken] = useState('');

  const [bucketKey, setBucketKey] = useState('');
  const [s3, setS3] = useState({
    region: '',
    endpoint: '',
    pathStyle: false,
    accessKeyId: '',
    secretKey: '',
  });
  const [path, setPath] = useState('');
  const [file, setFile] = useState<File>();
  const [sent, setSent] = useState(0);

  const [source, setSource] = useState<HostRestoreSource>();
  const [summary, setSummary] = useState<HostRestoreSummary>();
  const [confirmed, setConfirmed] = useState(false);
  const [startStacks, setStartStacks] = useState(true);
  const [lines, setLines] = useState<string[]>([]);
  const [waiting, setWaiting] = useState(false);

  const sse = useRef<SSEHandle | undefined>(undefined);
  const poll = useRef<ReturnType<typeof setInterval> | undefined>(undefined);
  const deadline = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(
    () => () => {
      sse.current?.close();
      clearInterval(poll.current);
      clearTimeout(deadline.current);
    },
    [],
  );

  function sourceFromForm(): HostRestoreSource | string {
    if (tab === 'path') return path.trim() ? { kind: 'path', path: path.trim() } : 'Enter the path of the archive on this machine.';
    if (tab === 'upload') return { kind: 'upload' };
    const { bucket, key } = splitBucketAndKey(bucketKey);
    if (!bucket || !key) return 'Enter the bucket and the full object key, or an s3:// URL.';
    const details: HostRestoreS3 = {
      bucket,
      key,
      region: s3.region.trim(),
      endpoint: s3.endpoint.trim(),
      pathStyle: s3.pathStyle,
      accessKeyId: s3.accessKeyId.trim(),
    };
    if (s3.secretKey) details.secretKey = s3.secretKey;
    return { kind: 's3', s3: details };
  }

  function explain(err: unknown): string {
    if (err instanceof ApiError && err.status === 403) {
      return 'A setup token is required unless you open this page from the machine itself. Enter the token shown in the server log.';
    }
    if (err instanceof ApiError && err.code === 'upload_running') {
      return 'Another upload is already in progress. Wait for it to finish, or reload the page and try again.';
    }
    if (err instanceof ApiError && err.code === 'restore_running') {
      return 'A restore is already running or waiting for Stackstead to restart. Wait for the restart, then reload this page.';
    }
    if (err instanceof ApiError && err.status === 409 && err.code === undefined) return 'Setup is already complete. Sign in instead.';
    return messageOf(err);
  }

  async function inspect() {
    if (busy) return;
    const chosen = sourceFromForm();
    if (typeof chosen === 'string') {
      setError(chosen);
      return;
    }
    if (tab === 'upload' && !file) {
      setError('Choose the archive file to upload.');
      return;
    }
    setBusy(true);
    setError('');
    setSent(0);
    try {
      if (tab === 'upload' && file) await setupRestore.upload(file, setupToken.trim() || undefined, setSent);
      const result = await setupRestore.inspect(chosen, setupToken.trim() || undefined);
      setSource(chosen);
      setSummary(result);
      setConfirmed(false);
      setStage('inspected');
    } catch (err) {
      setError(explain(err));
    } finally {
      setBusy(false);
    }
  }

  async function start() {
    if (busy || !confirmed || !source || !summary) return;
    setBusy(true);
    setError('');
    try {
      const started = await setupRestore.start(source, startStacks, summary.conflictsDigest, setupToken.trim() || undefined);
      setSetupToken('');
      setS3((v) => ({ ...v, secretKey: '' }));
      setStage('running');
      follow(started.restoreToken);
    } catch (err) {
      if (err instanceof ApiError && err.code === 'conflicts_changed') {
        // The host changed since the user looked: show the new conflicts and ask again.
        try {
          setSummary(await setupRestore.inspect(source, setupToken.trim() || undefined));
          setConfirmed(false);
          setError('Existing Docker state changed since you reviewed it. Review the conflicts again before starting.');
        } catch (inner) {
          setError(explain(inner));
        }
      } else {
        setError(explain(err));
      }
    } finally {
      setBusy(false);
    }
  }

  /**
   * The server re-executes itself once the restore is staged. A dropped stream is followed (a reconnect replays the
   * operation, so a failure still shows) while /api/setup/status is polled; a 403/404 means the new process answered.
   */
  function follow(token: string) {
    const stopWaiting = () => {
      clearInterval(poll.current);
      clearTimeout(deadline.current);
      poll.current = deadline.current = undefined;
    };
    // A live stream means the restore is still reporting: the wait (and its deadline) only measures silence.
    const streamIsLive = () => {
      stopWaiting();
      setWaiting(false);
    };
    function startWaiting() {
      setWaiting(true);
      if (poll.current !== undefined) return;
      poll.current = setInterval(() => {
        api.setupStatus().then(
          (s) => {
            if (s.needsSetup) return;
            stopWaiting();
            sse.current?.close();
            navigate('/login', { replace: true });
          },
          () => {
            /* the server is restarting */
          },
        );
      }, pollMs);
      deadline.current = setTimeout(() => {
        stopWaiting();
        sse.current?.close();
        setError('Stackstead did not come back — check the service log or supervisor.');
        setStage('failed');
      }, deadlineMs);
    }
    let lastLineId = -1;
    function onMessage(m: SSEMessage) {
      if (m.event === 'line') {
        // A reconnect may replay lines already shown; ids are absolute line indexes.
        const id = m.id === undefined ? NaN : Number(m.id);
        if (!Number.isNaN(id)) {
          if (id <= lastLineId) return;
          lastLineId = id;
        }
        streamIsLive();
        let text = m.data;
        try {
          const parsed: unknown = JSON.parse(m.data);
          if (typeof parsed === 'string') text = parsed;
        } catch {
          /* plain text */
        }
        setLines((prev) => [...prev, text]);
      } else if (m.event === 'done') {
        let op: { state?: string; error?: string } = {};
        try {
          op = JSON.parse(m.data) as typeof op;
        } catch {
          /* treat as succeeded: the status poll decides */
        }
        if (op.state && op.state !== 'succeeded') {
          stopWaiting();
          sse.current?.close();
          setError(op.error || `The restore ${op.state}. Nothing was installed; you can try again.`);
          setStage('failed');
        } else {
          sse.current?.close();
          startWaiting();
        }
      }
    }
    sse.current = openSSE({
      url: `/api/setup/restore/progress?token=${encodeURIComponent(token)}`,
      onMessage,
      onStatus: (status) => {
        if (status === 'open') streamIsLive();
        if (status === 'reconnecting') startWaiting();
        if (status === 'gone' || status === 'unauthorized') {
          sse.current?.close();
          startWaiting();
        }
      },
    });
  }

  if (stage === 'running') {
    return (
      <div className={styles.stack}>
        <AuthHint>Restoring this host. Keep this page open.</AuthHint>
        <ol className={styles.log} aria-label="Restore progress">
          {lines.map((line, i) => (
            <li key={i}>{line}</li>
          ))}
        </ol>
        {waiting && (
          <p role="status" className={styles.status}>
            Waiting for Stackstead to restart…
          </p>
        )}
      </div>
    );
  }

  if (stage === 'failed') {
    return (
      <div className={styles.stack}>
        <AuthError message={error} />
        <ol className={styles.log} aria-label="Restore progress">
          {lines.map((line, i) => (
            <li key={i}>{line}</li>
          ))}
        </ol>
        <Button
          variant="secondary"
          onClick={() => {
            setStage('source');
            setError('');
            setLines([]);
          }}
        >
          Start over
        </Button>
      </div>
    );
  }

  if (stage === 'inspected' && summary) {
    const { manifest, conflicts } = summary;
    return (
      <div className={styles.stack}>
        <AuthError message={error} />
        <dl className={styles.summary}>
          <dt>Host</dt>
          <dd>{manifest.host}</dd>
          <dt>Created</dt>
          <dd>{new Date(manifest.createdAt).toLocaleString()}</dd>
          <dt>Stackstead</dt>
          <dd>{manifest.stacksteadVersion}</dd>
          <dt>Status</dt>
          <dd>{manifest.status}</dd>
          <dt>Stacks</dt>
          <dd>{manifest.stacks.length ? manifest.stacks.map((s) => s.name).join(', ') : 'none'}</dd>
          <dt>Volumes</dt>
          <dd>{manifest.stacks.reduce((n, s) => n + s.volumes.length, 0)}</dd>
          <dt>Images</dt>
          <dd>{manifest.stacks.reduce((n, s) => n + s.images.length, 0)}</dd>
        </dl>
        <Banner kind="warning" title="This host will take over the backed-up host's identity; don't run both at once." />
        {conflicts.length === 0 ? (
          <p className={styles.muted}>Nothing on this host conflicts with the archive.</p>
        ) : (
          <>
            <h2 className={styles.heading}>Already on this host</h2>
            <ul className={styles.conflicts}>
              {conflicts.map((c) => (
                <li key={`${c.kind}/${c.name}`}>
                  <span className={styles.action} data-action={c.action}>
                    {c.action}
                  </span>
                  <span className={styles.kind}>{c.kind}</span>
                  <span className={styles.name}>{c.name}</span>
                </li>
              ))}
            </ul>
          </>
        )}
        <label className={styles.check}>
          <input type="checkbox" checked={startStacks} onChange={(e) => setStartStacks(e.target.checked)} />
          Start stacks that were running
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} />
          {conflicts.length === 0
            ? 'I understand this replaces the state of this host.'
            : 'I understand the items above are overwritten, recreated or kept as shown.'}
        </label>
        <div className={styles.actions}>
          <Button
            variant="ghost"
            onClick={() => {
              setStage('source');
              setError('');
            }}
          >
            Back
          </Button>
          <Button
            variant="danger"
            loading={busy}
            disabledReason={confirmed ? undefined : 'Confirm the conflicts first.'}
            onClick={() => void start()}
          >
            Start restore
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <AuthHint>Restore a whole-host backup instead of creating a new account. The admin account comes with the backup.</AuthHint>
      <AuthError message={error} />
      <Tabs tabs={SOURCE_TABS} selected={tab} ariaLabel="Archive source" onSelect={(id) => setTab(id as SourceTab)}>
        {(selected) => (
          <div className={styles.stack}>
            {selected === 's3' && (
              <>
                <Field
                  label="Bucket and key, or s3:// URL"
                  id="restore-s3-key"
                  mono
                  hint="The full object key, e.g. s3://my-bucket/host/stackstead-2026-10-26.tar.gz"
                >
                  {({ readOnly: _, ...c }) => (
                    <input {...c} value={bucketKey} autoComplete="off" spellCheck={false} onChange={(e) => setBucketKey(e.target.value)} />
                  )}
                </Field>
                <div className={styles.grid}>
                  <Field label="Region" id="restore-s3-region">
                    {({ readOnly: _, ...c }) => (
                      <input {...c} value={s3.region} onChange={(e) => setS3({ ...s3, region: e.target.value })} />
                    )}
                  </Field>
                  <Field label="Endpoint" id="restore-s3-endpoint" hint="Leave empty for AWS">
                    {({ readOnly: _, ...c }) => (
                      <input {...c} value={s3.endpoint} onChange={(e) => setS3({ ...s3, endpoint: e.target.value })} />
                    )}
                  </Field>
                  <Field label="Access key ID" id="restore-s3-akid" mono>
                    {({ readOnly: _, ...c }) => (
                      <input
                        {...c}
                        value={s3.accessKeyId}
                        autoComplete="off"
                        onChange={(e) => setS3({ ...s3, accessKeyId: e.target.value })}
                      />
                    )}
                  </Field>
                  <Field label="Secret access key" id="restore-s3-secret">
                    {({ readOnly: _, ...c }) => (
                      <input
                        {...c}
                        type="password"
                        value={s3.secretKey}
                        autoComplete="new-password"
                        onChange={(e) => setS3({ ...s3, secretKey: e.target.value })}
                      />
                    )}
                  </Field>
                </div>
                <label className={styles.check}>
                  <input type="checkbox" checked={s3.pathStyle} onChange={(e) => setS3({ ...s3, pathStyle: e.target.checked })} />
                  Path-style addressing
                </label>
              </>
            )}
            {selected === 'path' && (
              <Field label="Archive path" id="restore-path" mono hint="An absolute path to the .tar.gz on this machine.">
                {({ readOnly: _, ...c }) => (
                  <input {...c} value={path} autoComplete="off" spellCheck={false} onChange={(e) => setPath(e.target.value)} />
                )}
              </Field>
            )}
            {selected === 'upload' && (
              <>
                <Field label="Archive file" id="restore-file" hint="The archive is streamed to this machine and not held in memory.">
                  {({ readOnly: _, ...c }) => (
                    <input
                      {...c}
                      type="file"
                      accept=".gz,.tgz,application/gzip"
                      onChange={(e) => {
                        setFile(e.target.files?.[0]);
                        setSent(0);
                      }}
                    />
                  )}
                </Field>
                {busy && file && (
                  <progress className={styles.progress} aria-label="Upload progress" max={file.size} value={sent > 0 ? sent : undefined}>
                    {formatBytes(sent)}
                  </progress>
                )}
              </>
            )}
          </div>
        )}
      </Tabs>
      <Field label="Setup token (optional)" id="restore-token" hint="Needed unless you open this page from the machine itself.">
        {({ readOnly: _, ...c }) => (
          <input
            {...c}
            value={setupToken}
            type="text"
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
            onChange={(e) => setSetupToken(e.target.value)}
          />
        )}
      </Field>
      <Banner kind="info" title="Nothing changes until you confirm on the next step." />
      <Button variant="primary" loading={busy} onClick={() => void inspect()}>
        {tab === 'upload' ? 'Upload and inspect' : 'Inspect archive'}
      </Button>
    </div>
  );
}
