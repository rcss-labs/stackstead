import { useId, useState } from 'react';
import { useNavigate } from 'react-router';
import type { LibraryImageDetail, LibraryUse, UpdateImageRequest } from '$lib/api/imageTypes';
import { retentionText } from '$lib/images/retention';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field } from '../components/Field';
import { useServer } from '../servers/ServerContext';
import { argsToRows, BuildArgsEditor, rowsToArgs } from './buildArgs';
import styles from './images.module.css';
import { InUseBanner, inUseFrom } from './InUseBanner';
import { PushTargetsCard } from './PushTargetsCard';

const WHOLE = /^\d+$/;

export function ImageSettingsTab({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  const { api, href } = useServer();
  const navigate = useNavigate();
  const uid = useId();
  const [name, setName] = useState(image.name);
  const [dockerfilePath, setDockerfilePath] = useState(image.dockerfilePath);
  const [repo, setRepo] = useState(image.github?.repo ?? '');
  const [branch, setBranch] = useState(image.github?.branch ?? '');
  const [path, setPath] = useState(image.github?.path ?? '');
  const [rows, setRows] = useState(() => argsToRows(image.buildArgs));
  const [keepLast, setKeepLast] = useState(String(image.retention.keepLast));
  const [keepDays, setKeepDays] = useState(String(image.retention.keepDays));
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [inUse, setInUse] = useState<{ message: string; usedBy: LibraryUse[] }>();

  const lastError = WHOLE.test(keepLast) ? undefined : 'Enter a whole number, 0 or more';
  const daysError = WHOLE.test(keepDays) ? undefined : 'Enter a whole number, 0 or more';
  const retention = { keepLast: Number(keepLast) || 0, keepDays: Number(keepDays) || 0 };
  const invalidReason = lastError ?? daysError ?? (name.trim() === '' ? 'Enter a name' : undefined);

  async function save() {
    const request: UpdateImageRequest = { name: name.trim(), dockerfilePath: dockerfilePath.trim(), buildArgs: rowsToArgs(rows), retention };
    if (image.source === 'github') request.github = { repo: repo.trim(), branch: branch.trim(), path: path.trim() };
    setSaving(true);
    setSaveError(undefined);
    try {
      await api.updateImage(image.slug, request);
      toasts.push({ kind: 'success', message: 'Image settings saved' });
      onChanged();
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    setDeleting(true);
    try {
      await api.deleteImage(image.slug);
      toasts.push({ kind: 'success', message: `Deleted image ${image.name}` });
      void navigate(href('/images'));
    } catch (e) {
      const refused = inUseFrom(e);
      if (refused) setInUse(refused);
      else toasts.push({ kind: 'danger', message: `Could not delete the image: ${e instanceof Error ? e.message : String(e)}` });
      setConfirmDelete(false);
      setDeleting(false);
    }
  }

  return (
    <div className={styles.page}>
      <div className={styles.card}>
        <form
          className={styles.pad}
          onSubmit={(e) => {
            e.preventDefault();
            if (!invalidReason) void save();
          }}
        >
          <Field label="Name" id={`${uid}-name`}>
            {(control) => <input {...control} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="Slug" id={`${uid}-slug`} mono readonly readonlyReason="Part of every reference, so it never changes.">
            {(control) => <input {...control} value={image.slug} />}
          </Field>
          <Field label="Dockerfile path" id={`${uid}-dockerfile`} mono>
            {(control) => <input {...control} value={dockerfilePath} onChange={(e) => setDockerfilePath(e.target.value)} />}
          </Field>
          {image.source === 'github' && (
            <>
              <Field label="Repository" id={`${uid}-repo`} mono>
                {(control) => <input {...control} value={repo} onChange={(e) => setRepo(e.target.value)} />}
              </Field>
              <Field label="Branch" id={`${uid}-branch`} mono>
                {(control) => <input {...control} value={branch} onChange={(e) => setBranch(e.target.value)} />}
              </Field>
              <Field label="Path" id={`${uid}-path`} mono>
                {(control) => <input {...control} value={path} onChange={(e) => setPath(e.target.value)} />}
              </Field>
            </>
          )}
          <BuildArgsEditor title="Build arg defaults" rows={rows} onChange={setRows} />
          <p className={styles.muted}>Build arguments are stored and shown as plain text. They are not secrets.</p>
          <Field label="Keep last" id={`${uid}-last`} error={lastError} hint="Versions to keep. 0 keeps every version.">
            {(control) => <input {...control} inputMode="numeric" value={keepLast} onChange={(e) => setKeepLast(e.target.value)} />}
          </Field>
          <Field label="Keep days" id={`${uid}-days`} error={daysError} hint="Also keep anything newer than this many days. 0 turns the rule off.">
            {(control) => <input {...control} inputMode="numeric" value={keepDays} onChange={(e) => setKeepDays(e.target.value)} />}
          </Field>
          <p className={styles.muted}>{retentionText(retention)}</p>
          {saveError && (
            <Banner kind="danger" title="Could not save">
              {saveError}
            </Banner>
          )}
          <div>
            <Button variant="primary" type="submit" loading={saving} disabledReason={invalidReason}>
              Save
            </Button>
          </div>
        </form>
      </div>

      <PushTargetsCard image={image} onChanged={onChanged} />

      <div className={styles.card}>
        <div className={styles.pad}>
          <strong>Delete image</strong>
          <p className={styles.muted}>Removes the image, its files and every built version. Stacks using a version block the delete.</p>
          {inUse && <InUseBanner title="Image in use" message={inUse.message} usedBy={inUse.usedBy} />}
          <div>
            <Button variant="danger" onClick={() => setConfirmDelete(true)}>
              Delete image
            </Button>
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        title="Delete image"
        message={`Delete ${image.name} with all its versions and files? This cannot be undone.`}
        confirmLabel="Delete image"
        danger
        pending={deleting}
        onConfirm={() => void remove()}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  );
}
