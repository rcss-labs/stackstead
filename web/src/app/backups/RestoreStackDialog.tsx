import { useEffect, useId, useState } from 'react';
import type { BackupsApi } from '$lib/api/backups';
import type { BackupArchive, BackupManifest, BackupRestorePlan, BackupRestoreRequest } from '$lib/api/backupTypes';
import { formatBytes } from '$lib/format';
import { OperationPanel } from '../detail/OperationPanel';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { Spinner } from '../components/Spinner';
import { RestorePreview } from './RestorePreview';
import styles from './backups.module.css';

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));
const codeOf = (e: unknown) => (e as { code?: string } | null)?.code;

// The server answers this one with a plain 400 and no code.
const filesRequired = (message: string) => message.includes('restored with its files');

/** Restores one stack from an archive: choose parts, preview the plan, confirm, follow the operation. */
export function RestoreStackDialog({ api, archive, onClose }: { api: BackupsApi; archive: BackupArchive; onClose: () => void }) {
  const uid = useId();
  const [manifest, setManifest] = useState<BackupManifest>();
  const [loadError, setLoadError] = useState<string>();
  const [stack, setStack] = useState('');
  const [files, setFiles] = useState(true);
  const [volumes, setVolumes] = useState(false);
  const [images, setImages] = useState(false);
  const [skippedKeys, setSkippedKeys] = useState<Set<string>>(new Set());
  const [newName, setNewName] = useState('');
  const [offerRename, setOfferRename] = useState(false);
  const [plan, setPlan] = useState<BackupRestorePlan>();
  const [planning, setPlanning] = useState(false);
  const [error, setError] = useState<string>();
  const [confirming, setConfirming] = useState(false);
  const [starting, setStarting] = useState(false);
  const [operationId, setOperationId] = useState<string>();

  useEffect(() => {
    api.manifest(archive).then(
      (m) => {
        setManifest(m);
        setStack(m.stacks[0]?.name ?? '');
      },
      (e: unknown) => setLoadError(messageOf(e)),
    );
  }, [api, archive]);

  const manifestStack = manifest?.stacks.find((s) => s.name === stack);
  const archiveVolumes = manifestStack?.volumes ?? [];

  const request = (): BackupRestoreRequest => ({
    archive: { dest: archive.dest, name: archive.name },
    stack,
    newName: newName.trim(),
    files,
    volumes,
    images,
    volumeKeys: skippedKeys.size === 0 ? [] : archiveVolumes.filter((v) => !skippedKeys.has(v.key)).map((v) => v.key),
  });

  // Any change to the choices makes the shown plan stale.
  function change(update: () => void) {
    update();
    setPlan(undefined);
    setError(undefined);
  }

  function describeError(e: unknown): string {
    const message = messageOf(e);
    if (filesRequired(message)) {
      return 'This stack does not exist on this server, so it can only be restored together with its files. Tick Files and preview again.';
    }
    return message;
  }

  async function runPreview() {
    setPlanning(true);
    setError(undefined);
    setPlan(undefined);
    try {
      const next = await api.restorePlan(request());
      setPlan(next);
      if (next.overwrites || next.crossHost) setOfferRename(true);
    } catch (e) {
      if (codeOf(e) === 'target_occupied') setOfferRename(true);
      setError(describeError(e));
    } finally {
      setPlanning(false);
    }
  }

  async function start() {
    setStarting(true);
    try {
      const started = await api.restore(request());
      setConfirming(false);
      setOperationId(started.operation.id);
    } catch (e) {
      setConfirming(false);
      if (codeOf(e) === 'target_occupied') setOfferRename(true);
      setError(describeError(e));
      setPlan(undefined);
    } finally {
      setStarting(false);
    }
  }

  const noPartChosen = !files && !volumes && !images;
  const previewBlocked = !stack ? 'Choose a stack' : noPartChosen ? 'Choose at least one part to restore' : undefined;
  const replacesVolumes = volumes && (plan?.volumes.some((v) => !v.skip) ?? false);

  const confirmMessage = plan
    ? [
        plan.overwrites ? `This overwrites the existing stack ${plan.targetName} on this server.` : `This creates the stack ${plan.targetName}.`,
        files ? 'Its files are replaced with the archived copies.' : '',
        replacesVolumes ? 'Volume contents are replaced and cannot be recovered.' : '',
        images ? 'Archived images are loaded.' : '',
      ]
        .filter(Boolean)
        .join(' ')
    : '';

  return (
    <Dialog open title={`Restore from ${archive.name}`} onClose={onClose} width="lg">
      <div className={styles.restore}>
        {loadError && (
          <Banner kind="danger" title="Archive">
            {loadError}
          </Banner>
        )}
        {!manifest && !loadError && <Spinner />}
        {manifest && manifest.stacks.length === 0 && <p className={styles.muted}>This archive holds no stacks.</p>}
        {manifest && manifest.stacks.length > 0 && !operationId && (
          <>
            <Field label="Stack" id={`${uid}-stack`}>
              {(control) => (
                <select
                  {...control}
                  value={stack}
                  onChange={(e) =>
                    change(() => {
                      setStack(e.target.value);
                      setSkippedKeys(new Set());
                      setNewName('');
                      setOfferRename(false);
                    })
                  }
                >
                  {manifest.stacks.map((s) => (
                    <option key={s.name} value={s.name}>
                      {s.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>

            <div className={styles.parts}>
              <label className={styles.check}>
                <input type="checkbox" checked={files} onChange={(e) => change(() => setFiles(e.target.checked))} />
                Files
              </label>
              <label className={styles.check}>
                <input type="checkbox" checked={volumes} onChange={(e) => change(() => setVolumes(e.target.checked))} />
                Volumes
              </label>
              {volumes && (
                <ul className={styles.volumeList}>
                  {archiveVolumes.map((v) => (
                    <li key={v.key}>
                      <label className={styles.check}>
                        <input
                          type="checkbox"
                          checked={!skippedKeys.has(v.key)}
                          onChange={(e) =>
                            change(() =>
                              setSkippedKeys((current) => {
                                const next = new Set(current);
                                if (e.target.checked) next.delete(v.key);
                                else next.add(v.key);
                                return next;
                              }),
                            )
                          }
                        />
                        <span className={styles.mono}>{`${v.name} (${formatBytes(v.bytes)})`}</span>
                      </label>
                    </li>
                  ))}
                </ul>
              )}
              <label className={styles.check}>
                <input type="checkbox" checked={images} onChange={(e) => change(() => setImages(e.target.checked))} />
                Images
              </label>
            </div>

            {offerRename && (
              <Field
                label="Restore as new name"
                id={`${uid}-name`}
                hint="Leave empty to restore under the archived name. Use a new name to keep the stack that is here."
              >
                {(control) => <input {...control} type="text" value={newName} onChange={(e) => change(() => setNewName(e.target.value))} />}
              </Field>
            )}

            {error && (
              <Banner kind="danger" title="Restore">
                {error}
              </Banner>
            )}

            {plan && <RestorePreview plan={plan} replacesVolumes={replacesVolumes} />}

            <div className={styles.actions}>
              <Button variant="secondary" onClick={onClose}>
                Cancel
              </Button>
              <Button variant={plan ? 'secondary' : 'primary'} loading={planning} disabledReason={previewBlocked} onClick={() => void runPreview()}>
                Preview restore
              </Button>
              {plan && (
                <Button variant="danger" onClick={() => setConfirming(true)}>
                  Restore stack…
                </Button>
              )}
            </div>
          </>
        )}
        {operationId && (
          <>
            <OperationPanel operationId={operationId} />
            <div className={styles.actions}>
              <Button variant="secondary" onClick={onClose}>
                Close
              </Button>
            </div>
          </>
        )}
      </div>
      <ConfirmDialog
        open={confirming}
        title={`Restore ${plan?.targetName ?? stack}?`}
        message={confirmMessage}
        confirmLabel="Restore"
        danger
        pending={starting}
        onConfirm={() => void start()}
        onCancel={() => setConfirming(false)}
      />
    </Dialog>
  );
}
