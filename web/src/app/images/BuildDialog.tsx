import { useId, useState } from 'react';
import type { BuildStarted, LibraryImageDetail } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { useServer } from '../servers/ServerContext';
import { argsToRows, BuildArgsEditor, rowsToArgs } from './buildArgs';
import styles from './images.module.css';

export function BuildDialog(props: { image: LibraryImageDetail; onStarted(started: BuildStarted): void; onClose(): void }) {
  const { image, onStarted, onClose } = props;
  const { api } = useServer();
  const uid = useId();
  const [tag, setTag] = useState(image.suggestedTag);
  const [rows, setRows] = useState(() => argsToRows(image.buildArgs));
  const [saveArgs, setSaveArgs] = useState(false);
  const [ref, setRef] = useState('');
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError(undefined);
    try {
      onStarted(await api.buildImage(image.slug, { tag: tag.trim(), buildArgs: rowsToArgs(rows), saveArgs, ref: ref.trim() }));
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
          <Field label="Branch or commit" id={`${uid}-ref`} mono hint={`Empty builds ${image.github?.branch ?? 'the configured branch'}.`}>
            {(control) => <input {...control} value={ref} onChange={(e) => setRef(e.target.value)} />}
          </Field>
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
          <Button variant="primary" type="submit" loading={pending} disabledReason={tag.trim() === '' ? 'Enter a tag' : undefined}>
            Start build
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
