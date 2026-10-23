import { useId, useState } from 'react';
import type { CreateImageRequest, LibraryImage } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { useServer } from '../servers/ServerContext';
import styles from './images.module.css';

export function NewImageDialog({ onCreated, onClose }: { onCreated(image: LibraryImage): void; onClose(): void }) {
  const { api } = useServer();
  const uid = useId();
  const [name, setName] = useState('');
  const [source, setSource] = useState<'upload' | 'github'>('upload');
  const [dockerfilePath, setDockerfilePath] = useState('Dockerfile');
  const [repo, setRepo] = useState('');
  const [branch, setBranch] = useState('main');
  const [path, setPath] = useState('');
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending) return;
    const request: CreateImageRequest = { name: name.trim(), source, dockerfilePath: dockerfilePath.trim(), buildArgs: {} };
    if (source === 'github') request.github = { repo: repo.trim(), branch: branch.trim(), path: path.trim() };
    setPending(true);
    setError(undefined);
    try {
      onCreated(await api.createImage(request));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setPending(false);
    }
  }

  return (
    <Dialog open title="New image" onClose={onClose}>
      <form className={styles.form} onSubmit={(e) => void submit(e)}>
        <Field label="Name" id={`${uid}-name`} hint="The slug in every reference is derived from the name and never changes.">
          {(control) => <input {...control} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Source" id={`${uid}-source`}>
          {({ readOnly: _readOnly, ...control }) => (
            <select {...control} value={source} onChange={(e) => setSource(e.target.value as 'upload' | 'github')}>
              <option value="upload">Upload files</option>
              <option value="github">GitHub repository</option>
            </select>
          )}
        </Field>
        {source === 'github' && (
          <>
            <Field label="Repository" id={`${uid}-repo`} hint="owner/name" mono>
              {(control) => <input {...control} value={repo} onChange={(e) => setRepo(e.target.value)} />}
            </Field>
            <Field label="Branch" id={`${uid}-branch`} mono>
              {(control) => <input {...control} value={branch} onChange={(e) => setBranch(e.target.value)} />}
            </Field>
            <Field label="Path" id={`${uid}-path`} hint="Empty means the repository root." mono>
              {(control) => <input {...control} value={path} onChange={(e) => setPath(e.target.value)} />}
            </Field>
          </>
        )}
        <Field label="Dockerfile path" id={`${uid}-dockerfile`} mono>
          {(control) => <input {...control} value={dockerfilePath} onChange={(e) => setDockerfilePath(e.target.value)} />}
        </Field>
        {error && (
          <Banner kind="danger" title="Could not create the image">
            {error}
          </Banner>
        )}
        <div className={styles.actions}>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={pending} disabledReason={name.trim() === '' ? 'Enter a name' : undefined}>
            Create
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
