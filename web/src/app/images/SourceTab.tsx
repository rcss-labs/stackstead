import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { Link } from 'react-router';
import { ApiError } from '$lib/api/client';
import type { GitHubHead, ImageFile, LibraryImageDetail } from '$lib/api/imageTypes';
import { createComposeEditor } from '$lib/stacks/compose';
import { get } from '$lib/store';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';
import { DiffView } from '../stacks/DiffView';
import { YamlEditor } from '../stacks/YamlEditor';
import { FileTree } from './FileTree';
import styles from './images.module.css';

export function SourceTab({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  return image.source === 'github' ? <GitHubSource image={image} /> : <UploadSource image={image} onChanged={onChanged} />;
}

const encodePath = (p: string) => p.split('/').map(encodeURIComponent).join('/');

function GitHubSource({ image }: { image: LibraryImageDetail }) {
  const { api, href } = useServer();
  const [head, setHead] = useState<GitHubHead>();
  const [error, setError] = useState<string>();
  const [needsToken, setNeedsToken] = useState(false);
  const [loading, setLoading] = useState(true);
  const slug = image.slug;

  const load = useCallback(async () => {
    setLoading(true);
    setError(undefined);
    setHead(undefined);
    setNeedsToken(false);
    try {
      setHead(await api.githubHead(slug));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setNeedsToken(e instanceof ApiError && (e.code === 'github_unconfigured' || e.code === 'github_unauthorized'));
    } finally {
      setLoading(false);
    }
  }, [api, slug]);
  useEffect(() => {
    void load();
  }, [load]);

  const gh = image.github;
  return (
    <div className={styles.card}>
      <div className={styles.pad}>
        <p>
          Built from <span className={styles.mono}>{gh?.repo}</span> on <span className={styles.mono}>{gh?.branch}</span>
          {gh?.path ? (
            <>
              {' '}in <span className={styles.mono}>{gh.path}</span>
            </>
          ) : null}
          , Dockerfile <span className={styles.mono}>{image.dockerfilePath}</span>. Edit the source in the repository; change where it is read from in Settings.
        </p>
        {loading && <p role="status">Loading Dockerfile from GitHub…</p>}
        {error && (
          <Banner kind="danger" title="Could not reach GitHub">
            {error}
            {needsToken && (
              <>
                {' '}
                Check the GitHub token in <Link to={href('/settings#settings-github')}>Settings → GitHub</Link>.
              </>
            )}
          </Banner>
        )}
        {head && (
          <>
            <div className={styles.row}>
              <span>
                Branch head <span className={styles.mono}>{head.commit.slice(0, 7)}</span>
              </span>
              <span className={styles.spacer} />
              <a href={`https://github.com/${encodePath(gh?.repo ?? '')}/blob/${head.commit}/${encodePath(head.path)}`} target="_blank" rel="noreferrer noopener">
                Open on GitHub
              </a>
            </div>
            <pre className={styles.log} aria-label="Dockerfile at branch head">
              {head.dockerfile}
            </pre>
          </>
        )}
        <div>
          <Button variant="secondary" size="sm" loading={loading} onClick={() => void load()}>
            Refresh
          </Button>
        </div>
      </div>
    </div>
  );
}

const CONFLICT_REASON = 'Resolve the conflict first (Reload or Discard)';

function UploadSource({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  const { api } = useServer();
  const slug = image.slug;
  const [files, setFiles] = useState<ImageFile[]>([]);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // The editor's "hash" is the image revision the Dockerfile was loaded at.
  const [editor] = useState(() =>
    createComposeEditor(slug, {
      load: async () => {
        const d = await api.getImageDockerfile(slug);
        return { content: d.content, hash: String(d.revision) };
      },
      save: async (content, hash) => ({ hash: String((await api.saveImageDockerfile(slug, content, Number(hash))).number) }),
    }),
  );
  const st = useStore(editor.state);

  const loadFiles = useCallback(async () => {
    try {
      const list = await api.imageFiles(slug);
      if (mounted.current) setFiles(list.files);
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not load the files: ${e instanceof Error ? e.message : String(e)}` });
    }
  }, [api, slug]);

  useEffect(() => {
    void editor.reload();
    void loadFiles();
  }, [editor, loadFiles]);

  const loading = st.phase === 'loading' || st.phase === 'loadFailed';
  const isDirty = st.draft !== st.content;
  const conflict = st.phase === 'conflict';
  const previewing = st.phase === 'previewing';
  const saving = st.phase === 'saving';

  useEffect(() => {
    setUnsavedChanges(isDirty);
    return () => setUnsavedChanges(false);
  }, [isDirty]);

  async function save(): Promise<void> {
    if (!isDirty || conflict || saving || loading) return;
    if (await editor.save()) {
      toasts.push({ kind: 'success', message: 'Dockerfile saved' });
      onChanged();
      void loadFiles();
    }
  }

  /** A file change makes a new revision, so the loaded Dockerfile is behind: reload it, keeping edits. */
  async function fileChange(action: () => Promise<unknown>, done: string) {
    setBusy(true);
    try {
      await action();
      toasts.push({ kind: 'success', message: done });
      const dirty = get(editor.state).draft !== get(editor.state).content;
      await Promise.all([loadFiles(), editor.reload({ keepDraft: dirty })]);
      onChanged();
    } catch (e) {
      toasts.push({ kind: 'danger', message: e instanceof ApiError ? e.message : `Failed: ${e instanceof Error ? e.message : String(e)}` });
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    }
  }

  return (
    <div className={styles.page}>
      <div className={styles.card} onKeyDown={onKeyDown}>
        <div className={styles.pad}>
          <div className={styles.row}>
            <strong className={styles.mono}>{image.dockerfilePath}</strong>
            {isDirty && <span className={styles.muted}>· modified</span>}
            <span className={styles.spacer} />
            {previewing ? (
              <Button variant="secondary" size="sm" onClick={() => editor.cancelPreview()}>
                Back to editing
              </Button>
            ) : (
              <Button variant="secondary" size="sm" disabledReason={!isDirty ? 'No changes to preview' : conflict ? CONFLICT_REASON : undefined} onClick={() => editor.preview()}>
                Preview changes
              </Button>
            )}
            <Button variant="primary" size="sm" loading={saving} disabledReason={!isDirty ? 'No changes to save' : conflict ? CONFLICT_REASON : undefined} onClick={() => void save()}>
              Save
            </Button>
          </div>

          {st.phase === 'loading' && <p role="status">Loading Dockerfile…</p>}
          {st.phase === 'loadFailed' && (
            <>
              <Banner kind="danger" title="Could not load the Dockerfile">
                {st.error}
              </Banner>
              <div>
                <Button variant="secondary" onClick={() => void editor.reload()}>
                  Retry
                </Button>
              </div>
            </>
          )}
          {conflict && (
            <>
              <Banner kind="danger" title="Save refused">
                This Dockerfile changed since you loaded it, so your save was not applied.
              </Banner>
              <div className={styles.row}>
                <Button variant="primary" onClick={() => void editor.reload({ keepDraft: true })}>
                  Reload
                </Button>
                <Button variant="secondary" onClick={() => void editor.reload({ keepDraft: false })}>
                  Discard my changes
                </Button>
              </div>
            </>
          )}
          {previewing && <DiffView original={st.content} modified={st.draft} ariaLabel="Changes to the Dockerfile" language="dockerfile" />}
          {!loading && (
            <div hidden={previewing}>
              <YamlEditor value={st.draft} original={st.content} language="dockerfile" ariaLabel="Dockerfile" onChange={(v) => editor.setDraft(v)} />
            </div>
          )}
          {st.phase === 'editing' && st.error && <pre className={styles.log}>{st.error}</pre>}
        </div>
      </div>

      <div className={styles.card} aria-busy={busy || undefined}>
        <FileTree
          files={files}
          onUpload={(picked) => void fileChange(() => api.uploadImageFiles(slug, picked), `Uploaded ${picked.length} file${picked.length === 1 ? '' : 's'}`)}
          onRename={(from, to) => void fileChange(() => api.renameImageFile(slug, { from, to }), `Renamed ${from}`)}
          onDelete={(path) => void fileChange(() => api.deleteImageFile(slug, path), `Deleted ${path}`)}
        />
      </div>
    </div>
  );
}
