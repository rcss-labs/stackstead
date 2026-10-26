import { useEffect, useMemo, useState } from 'react';
import { createBackupsApi } from '$lib/api/backups';
import type { BackupArchive } from '$lib/api/backupTypes';
import { formatBytes } from '$lib/format';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { Tabs } from '../components/Tabs';
import { useServer } from '../servers/ServerContext';
import { BackupRunsList } from './BackupRunsList';
import { BackupScheduleCard } from './BackupScheduleCard';
import { BackupStacksTable } from './BackupStacksTable';
import styles from './backups.module.css';

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Groups archives by host, with `own` first and the rest by name. */
function groupByHost(archives: BackupArchive[], own: string): { host: string; archives: BackupArchive[] }[] {
  const groups = new Map<string, BackupArchive[]>();
  for (const a of archives) groups.set(a.hostSlug, [...(groups.get(a.hostSlug) ?? []), a]);
  return [...groups.entries()]
    .sort(([a], [b]) => (a === own ? -1 : b === own ? 1 : a.localeCompare(b)))
    .map(([host, list]) => ({ host, archives: list }));
}

function ArchivesTab({ api, ownSlug }: { api: ReturnType<typeof createBackupsApi>; ownSlug: string }) {
  const [archives, setArchives] = useState<BackupArchive[]>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    api.archives().then(setArchives, (e: unknown) => setError(messageOf(e)));
  }, [api]);

  if (error)
    return (
      <Banner kind="danger" title="Archives">
        {error}
      </Banner>
    );
  if (!archives) return <Spinner />;
  if (archives.length === 0) return <EmptyState title="No archives" description="Archives appear here after the first backup." />;

  return (
    <div className={styles.stack}>
      {groupByHost(archives, ownSlug).map(({ host, archives: list }) => (
        <section key={host} className={styles.host} aria-label={host}>
          <h2 className={styles.hostTitle}>{host === ownSlug ? `${host} (this host)` : host}</h2>
          <div className={styles.scroll}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">Archive</th>
                  <th scope="col">Where</th>
                  <th scope="col">Created</th>
                  <th scope="col">Size</th>
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {list.map((a) => (
                  <tr key={`${a.dest}:${a.name}`}>
                    <td className={styles.mono}>{a.name}</td>
                    <td>{a.dest === 's3' ? 'S3' : 'Local'}</td>
                    <td>
                      <RelativeTime iso={a.createdAt} />
                    </td>
                    <td>{formatBytes(a.size)}</td>
                    <td>
                      <Button variant="secondary" size="sm" disabledReason="Restore is not available yet">
                        Restore
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ))}
    </div>
  );
}

/** Backups of the viewed server: its archives, and its schedule, stacks and runs. */
export function BackupsScreen() {
  const scope = useServer();
  const base = scope.local ? '' : `/api/servers/${encodeURIComponent(scope.id)}`;
  const api = useMemo(() => createBackupsApi(base), [base]);
  const [tab, setTab] = useState('archives');
  const [runsKey, setRunsKey] = useState(0);
  const [ownSlug, setOwnSlug] = useState('');

  useEffect(() => {
    document.title = 'Backups · Stackstead';
  }, []);

  // Archives are tagged with the identity slug, which can differ from the URL slug a peer is opened under.
  useEffect(() => {
    api.self().then((self) => setOwnSlug(self.slug), () => undefined);
  }, [api]);

  return (
    <section className={styles.page} aria-labelledby="backups-heading">
      <header className={styles.head}>
        <h1 id="backups-heading" className={styles.title}>
          Backups
        </h1>
      </header>
      <Tabs
        ariaLabel="Backups"
        tabs={[
          { id: 'archives', label: 'Archives' },
          { id: 'schedule', label: 'Schedule' },
        ]}
        selected={tab}
        onSelect={(id) => setTab(id)}
      >
        {(selected) =>
          selected === 'archives' ? (
            <ArchivesTab api={api} ownSlug={ownSlug} />
          ) : (
            <div className={styles.stack}>
              <BackupScheduleCard api={api} onRunStarted={() => setRunsKey((k) => k + 1)} />
              <BackupStacksTable api={api} />
              <BackupRunsList api={api} refreshKey={runsKey} />
            </div>
          )
        }
      </Tabs>
    </section>
  );
}
