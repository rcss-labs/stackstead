import { Fragment, useEffect, useState } from 'react';
import type { LibraryImageDetail, LibraryUse, LibraryVersion, LibraryVersionDetail } from '$lib/api/imageTypes';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { EmptyState } from '../components/EmptyState';
import { useServer } from '../servers/ServerContext';
import styles from './images.module.css';
import { InUseBanner, inUseFrom } from './InUseBanner';
import { VersionStatus } from './VersionStatus';

const argsText = (args: Record<string, string>) =>
  Object.entries(args)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ') || '—';

const sourceText = (v: LibraryVersion) => (v.commit ? v.commit.slice(0, 7) : `r${v.revision}`);

export function VersionsTab({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  const { api } = useServer();
  const slug = image.slug;
  // Detail (the build log can be large, and it carries the used-by list) loads only when a row is opened.
  const [details, setDetails] = useState<Record<string, LibraryVersionDetail>>({});
  const [detailErrors, setDetailErrors] = useState<Record<string, string>>({});
  const [open, setOpen] = useState<string>();
  const [deleting, setDeleting] = useState<LibraryVersion>();
  const [pending, setPending] = useState(false);
  const [inUse, setInUse] = useState<{ message: string; usedBy: LibraryUse[] }>();

  // Versions are keyed by id: failed attempts can share a tag with each other and with the live version.
  useEffect(() => {
    if (open === undefined) return;
    let cancelled = false;
    const id = open;
    api
      .imageVersion(slug, id)
      .then((d) => {
        if (cancelled) return;
        setDetails((prev) => ({ ...prev, [id]: d }));
        setDetailErrors(({ [id]: _gone, ...rest }) => rest);
      })
      .catch((e: unknown) => {
        if (!cancelled) setDetailErrors((prev) => ({ ...prev, [id]: e instanceof Error ? e.message : String(e) }));
      });
    return () => {
      cancelled = true;
    };
  }, [api, slug, open]);

  async function remove(version: LibraryVersion) {
    setPending(true);
    setInUse(undefined);
    try {
      await api.deleteImageVersion(slug, version.id);
      toasts.push({ kind: 'success', message: `Deleted ${version.ref}` });
      setDeleting(undefined);
      onChanged();
    } catch (e) {
      const refused = inUseFrom(e);
      if (refused) setInUse(refused);
      else toasts.push({ kind: 'danger', message: `Could not delete the version: ${e instanceof Error ? e.message : String(e)}` });
      setDeleting(undefined);
    } finally {
      setPending(false);
    }
  }

  if (image.versions.length === 0) {
    return (
      <div className={styles.card}>
        <EmptyState title="No versions yet" description="Build the image to create its first version." />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      {inUse && <InUseBanner title="Version in use" message={inUse.message} usedBy={inUse.usedBy} />}
      <div className={`${styles.card} ${styles.scroll}`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Tag</th>
              <th>Source</th>
              <th>Arch</th>
              <th>Build args</th>
              <th>Status</th>
              <th>
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {image.versions.map((v) => {
              const detail = details[v.id];
              const detailError = detailErrors[v.id];
              const isOpen = open === v.id;
              return (
                <Fragment key={v.id}>
                  <tr>
                    <td className={styles.mono}>{v.tag}</td>
                    <td className={styles.mono}>{sourceText(v)}</td>
                    <td>{v.arch}</td>
                    <td className={styles.mono}>{argsText(v.buildArgs)}</td>
                    <td>
                      <VersionStatus status={v.status} />
                    </td>
                    <td className={styles.actionsCell}>
                      <Button variant="ghost" size="xs" aria-label={`Open ${v.tag} (${v.status}, started ${v.startedAt})`} aria-expanded={isOpen} onClick={() => setOpen(isOpen ? undefined : v.id)}>
                        Open
                      </Button>
                      <Button variant="ghost" size="xs" aria-label={`Delete ${v.tag} (${v.status}, started ${v.startedAt})`} onClick={() => setDeleting(v)}>
                        Delete
                      </Button>
                    </td>
                  </tr>
                  {isOpen && (
                    <tr>
                      <td colSpan={6}>
                        <div className={styles.form}>
                          {(detail?.error || v.error) && (
                            <Banner kind="danger" title="Build failed">
                              {detail?.error || v.error}
                            </Banner>
                          )}
                          <span className={styles.mono}>{v.ref}</span>
                          {detailError && (
                            <Banner kind="danger" title="Could not load the version">
                              {detailError}
                            </Banner>
                          )}
                          {detail && (
                            <p>
                              <strong>Used by {detail.usedBy.length} service{detail.usedBy.length === 1 ? '' : 's'}</strong>
                            </p>
                          )}
                          {detail && detail.usedBy.length > 0 && (
                            <ul className={styles.usedBy}>
                              {detail.usedBy.map((u) => (
                                <li key={`${u.server}/${u.stack}/${u.service}`}>
                                  {u.stack} / {u.service}
                                </li>
                              ))}
                            </ul>
                          )}
                          {detail ? (
                            <pre className={styles.log} role="log" aria-label={`Build log for ${v.tag}`} tabIndex={0}>
                              {detail.log}
                            </pre>
                          ) : (
                            !detailError && <p role="status">Loading log…</p>
                          )}
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className={styles.muted}>Build args are stored and shown as plain text. They are not secrets.</p>
      <ConfirmDialog
        open={deleting !== undefined}
        title="Delete version"
        message={`Delete ${deleting?.ref ?? ''}? The built image is removed from this server.`}
        confirmLabel="Delete version"
        danger
        pending={pending}
        onConfirm={() => deleting && void remove(deleting)}
        onCancel={() => setDeleting(undefined)}
      />
    </div>
  );
}
