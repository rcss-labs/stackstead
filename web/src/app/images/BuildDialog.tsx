import { useId, useState } from 'react';
import type { BuildStarted, LibraryImageDetail } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { Segmented } from '../components/Segmented';
import { useServer } from '../servers/ServerContext';
import { argsToRows, BuildArgsEditor, rowsToArgs } from './buildArgs';
import styles from './images.module.css';

const COMMIT = /^[0-9a-fA-F]{7,40}$/;

export function BuildDialog(props: { image: LibraryImageDetail; onStarted(started: BuildStarted): void; onClose(): void }) {
  const { image, onStarted, onClose } = props;
  const { api } = useServer();
  const uid = useId();
  const [tag, setTag] = useState(image.suggestedTag);
  const [rows, setRows] = useState(() => argsToRows(image.buildArgs));
  const [saveArgs, setSaveArgs] = useState(false);
  const [target, setTarget] = useState<'head' | 'commit'>('head');
  const [commit, setCommit] = useState('');
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  const commitError = target === 'commit' && !COMMIT.test(commit.trim()) ? 'Enter 7 to 40 hex characters' : undefined;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending || commitError) return;
    setPending(true);
    setError(undefined);
    try {
      onStarted(await api.buildImage(image.slug, { tag: tag.trim(), buildArgs: rowsToArgs(rows), saveArgs, ref: target === 'commit' ? commit.trim() : '' }));
    } catch (e) {
      // build_running, tag_taken, arch_mismatch and the rest arrive with their own message.
      setError(e instanceof Error ? e.message : String(e));
      setPending(false);
    }
  }

  return (
    <Dialog open title="Build image" onClose={onClose}>
      <form className={styles.form} onSubmit={(e) => void submit(e)}>
        <Field label="Tag" id={`${uid}-tag`} mono hint={image.tagHint ? `The latest tag is ${image.tagHint}; pick the next one.` : undefined}>
          {(control) => <input {...control} value={tag} onChange={(e) => setTag(e.target.value)} />}
        </Field>
        {image.source === 'github' && (
          <>
            <Segmented
              label="Build from"
              options={[
                { value: 'head', label: 'Branch head' },
                { value: 'commit', label: 'Commit' },
              ]}
              value={target}
              onChange={(v) => setTarget(v as 'head' | 'commit')}
            />
            {target === 'head' ? (
              <p className={styles.muted}>Builds the latest commit on {image.github?.branch ?? 'the configured branch'}.</p>
            ) : (
              <Field label="Commit" id={`${uid}-ref`} mono error={commit === '' ? undefined : commitError} hint="A full or abbreviated commit SHA.">
                {(control) => <input {...control} value={commit} onChange={(e) => setCommit(e.target.value)} />}
              </Field>
            )}
          </>
        )}
        <BuildArgsEditor rows={rows} onChange={setRows} />
        <p className={styles.muted}>Build arguments are stored and shown as plain text. They are not secrets.</p>
        <label className={styles.checkbox}>
          <input type="checkbox" checked={saveArgs} onChange={(e) => setSaveArgs(e.target.checked)} />
          Save these as the image's defaults
        </label>
        {error && (
          <Banner kind="danger" title="Build not started">
            {error}
          </Banner>
        )}
        <div className={styles.actions}>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={pending} disabledReason={tag.trim() === '' ? 'Enter a tag' : commitError}>
            Start build
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
