import { useEffect, useId, useState, type FormEvent } from 'react';
import type { LibraryImageDetail, PushTarget, RegistryOption } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { useServer } from '../servers/ServerContext';
import styles from './images.module.css';

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

function TargetDialog(props: { image: LibraryImageDetail; target?: PushTarget; onSaved(): void; onClose(): void }) {
  const { image, target, onSaved, onClose } = props;
  const { api } = useServer();
  const uid = useId();
  const [registries, setRegistries] = useState<RegistryOption[]>();
  const [registryId, setRegistryId] = useState(target?.registryId ?? '');
  const [repository, setRepository] = useState(target?.repository ?? '');
  const [autoPush, setAutoPush] = useState(target?.autoPush ?? false);
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  useEffect(() => {
    api.imageRegistries().then(setRegistries, (e: unknown) => setError(messageOf(e)));
  }, [api]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending) return;
    const request = { registryId, repository: repository.trim(), autoPush };
    setPending(true);
    setError(undefined);
    try {
      if (target) await api.updatePushTarget(image.slug, target.id, request);
      else await api.addPushTarget(image.slug, request);
      onSaved();
    } catch (e) {
      setError(messageOf(e));
      setPending(false);
    }
  }

  const invalid = registryId === '' ? 'Pick a registry' : repository.trim() === '' ? 'Enter a repository' : undefined;
  return (
    <Dialog open title={target ? 'Edit push target' : 'Add push target'} onClose={onClose}>
      <form className={styles.form} onSubmit={(e) => void submit(e)}>
        <Field label="Registry" id={`${uid}-registry`} hint={registries?.length === 0 ? 'No registries are configured. Add one in Settings.' : undefined}>
          {({ readOnly: _, ...c }) => (
            <select {...c} value={registryId} onChange={(e) => setRegistryId(e.target.value)}>
              <option value="">Choose a registry</option>
              {registries?.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
          )}
        </Field>
        <Field label="Repository" id={`${uid}-repository`} mono hint="For example user/app (Docker Hub) or the ECR repository name.">
          {(c) => <input {...c} value={repository} onChange={(e) => setRepository(e.target.value)} />}
        </Field>
        <label className={styles.checkbox}>
          <input type="checkbox" checked={autoPush} onChange={(e) => setAutoPush(e.target.checked)} />
          Push automatically after each successful build
        </label>
        {error && (
          <Banner kind="danger" title="Could not save">
            {error}
          </Banner>
        )}
        <div className={styles.actions}>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={pending} disabledReason={invalid}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function PushTargetsCard({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  const { api } = useServer();
  const [dialog, setDialog] = useState<{ target?: PushTarget }>();
  const [removing, setRemoving] = useState<PushTarget>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();

  async function remove(target: PushTarget) {
    setPending(true);
    try {
      await api.deletePushTarget(image.slug, target.id);
      setRemoving(undefined);
      onChanged();
    } catch (e) {
      setError(messageOf(e));
      setRemoving(undefined);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className={styles.card}>
      <div className={styles.pad}>
        <div className={styles.row}>
          <strong>Push targets</strong>
          <span className={styles.spacer} />
          <Button variant="secondary" size="xs" onClick={() => setDialog({})}>
            Add push target
          </Button>
        </div>
        <p className={styles.muted}>Registries this image can be pushed to. Registries are configured in Settings.</p>
        {error && (
          <Banner kind="danger" title="Push targets">
            {error}
          </Banner>
        )}
        {image.pushTargets.length === 0 && <p className={styles.muted}>No push targets yet.</p>}
        {image.pushTargets.map((t) => (
          <div key={t.id} className={styles.row}>
            <span>{t.registryName}</span>
            <span className={styles.mono}>{t.repository}</span>
            <span className={styles.muted}>{t.autoPush ? 'Auto-push on' : 'Auto-push off'}</span>
            <span className={styles.spacer} />
            <Button variant="ghost" size="xs" aria-label={`Edit ${t.repository}`} onClick={() => setDialog({ target: t })}>
              Edit
            </Button>
            <Button variant="ghost" size="xs" aria-label={`Remove ${t.repository}`} onClick={() => setRemoving(t)}>
              Remove
            </Button>
          </div>
        ))}
      </div>
      {dialog && (
        <TargetDialog
          image={image}
          target={dialog.target}
          onClose={() => setDialog(undefined)}
          onSaved={() => {
            setDialog(undefined);
            onChanged();
          }}
        />
      )}
      <ConfirmDialog
        open={removing !== undefined}
        title="Remove push target"
        message={`Stop pushing to ${removing?.registryName ?? ''} ${removing?.repository ?? ''}? Images already pushed stay in the registry.`}
        confirmLabel="Remove target"
        danger
        pending={pending}
        onConfirm={() => removing && void remove(removing)}
        onCancel={() => setRemoving(undefined)}
      />
    </div>
  );
}
