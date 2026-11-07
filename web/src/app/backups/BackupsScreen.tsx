import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { createBackupsApi } from "$lib/api/backups";
import type { BackupArchive, BackupConfig } from "$lib/api/backupTypes";
import { formatBytes } from "$lib/format";
import { toasts } from "$lib/stores/toasts";
import { Banner } from "../components/Banner";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { RelativeTime } from "../components/RelativeTime";
import { Spinner } from "../components/Spinner";
import { Tag } from "../components/Tag";
import { Tabs } from "../components/Tabs";
import { useIsMobile } from "../hooks/useIsMobile";
import { useStore } from "../hooks/useStore";
import { useServer } from "../servers/ServerContext";
import { BackupRunsList } from "./BackupRunsList";
import { BackupScheduleCard } from "./BackupScheduleCard";
import { BackupStacksTable } from "./BackupStacksTable";
import { RestoreStackDialog } from "./RestoreStackDialog";
import { backupHeadline, type RunsState } from "./status";
import styles from "./backups.module.css";

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Groups archives by host, with `own` first and the rest by name. */
function groupByHost(
  archives: BackupArchive[],
  own: string,
): { host: string; archives: BackupArchive[] }[] {
  const groups = new Map<string, BackupArchive[]>();
  for (const a of archives)
    groups.set(a.hostSlug, [...(groups.get(a.hostSlug) ?? []), a]);
  return [...groups.entries()]
    .sort(([a], [b]) => (a === own ? -1 : b === own ? 1 : a.localeCompare(b)))
    .map(([host, list]) => ({ host, archives: list }));
}

function ArchivesTab({
  api,
  ownSlug,
  runs,
}: {
  api: ReturnType<typeof createBackupsApi>;
  ownSlug: string;
  runs: RunsState;
}) {
  const [archives, setArchives] = useState<BackupArchive[]>();
  const [error, setError] = useState<string>();
  const [restoring, setRestoring] = useState<BackupArchive>();
  const mobile = useIsMobile();

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
  const unencrypted = archives.filter((a) => !a.encrypted).length;

  return (
    <div className={styles.stack}>
      {archives.length === 0 && (
        <EmptyState
          title="No archives"
          description="Archives appear here after the first backup."
        />
      )}
      {unencrypted > 0 && (
        <Banner
          kind="warning"
          title={`${unencrypted} ${unencrypted === 1 ? "archive is" : "archives are"} not encrypted`}
        >
          Anyone who can read an unencrypted archive can sign in as the admin.
          Check before restoring from one.
        </Banner>
      )}
      {groupByHost(archives, ownSlug).map(({ host, archives: list }) => (
        <section key={host} className={styles.host} aria-label={host}>
          <h2 className={styles.hostTitle}>
            {host === ownSlug ? `${host} (this host)` : host}
          </h2>
          {mobile ? (
            <ul
              className={styles.archiveList}
              aria-label={`Archives of ${host}`}
            >
              {list.map((a) => (
                <li key={`${a.dest}:${a.name}`}>
                  <button
                    type="button"
                    className={styles.archiveRow}
                    aria-label={`Restore ${a.name}`}
                    onClick={() => setRestoring(a)}
                  >
                    <span className={styles.archiveText}>
                      <span className={styles.mono}>{a.name}</span>
                      <span className={styles.muted}>
                        <RelativeTime iso={a.createdAt} /> ·{" "}
                        {formatBytes(a.size)} ·{" "}
                        {a.dest === "s3" ? "S3" : "Local"} ·{" "}
                        <Tag
                          tone={a.encrypted ? "success" : "warning"}
                          mono={false}
                        >
                          {a.encrypted ? "Encrypted" : "Unencrypted"}
                        </Tag>
                      </span>
                    </span>
                    <span aria-hidden="true">›</span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
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
                      <td className={styles.mono}>
                        {a.name}{" "}
                        <Tag
                          tone={a.encrypted ? "success" : "warning"}
                          mono={false}
                        >
                          {a.encrypted ? "Encrypted" : "Unencrypted"}
                        </Tag>
                      </td>
                      <td>{a.dest === "s3" ? "S3" : "Local"}</td>
                      <td>
                        <RelativeTime iso={a.createdAt} />
                      </td>
                      <td>{formatBytes(a.size)}</td>
                      <td>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setRestoring(a)}
                        >
                          Restore
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      ))}
      <section className={styles.host} aria-label="Recent runs">
        <h2 className={styles.hostTitle}>Recent runs</h2>
        <BackupRunsList state={runs} />
      </section>
      {restoring && (
        <RestoreStackDialog
          key={`${restoring.dest}:${restoring.name}`}
          api={api}
          archive={restoring}
          onClose={() => setRestoring(undefined)}
        />
      )}
    </div>
  );
}

const TABS = [
  { id: "archives", label: "Archives" },
  { id: "stacks", label: "Stacks" },
  { id: "schedule", label: "Schedule" },
];

/** Backups of the viewed server: how the last run went, its archives, per-stack coverage and its schedule. */
export function BackupsScreen() {
  const scope = useServer();
  const base = scope.local
    ? ""
    : `/api/servers/${encodeURIComponent(scope.id)}`;
  const api = useMemo(() => createBackupsApi(base), [base]);
  const opsTick = useStore(scope.operationsRefresh);
  const [query, setQuery] = useSearchParams();
  const requested = query.get("tab");
  const tab = TABS.some((t) => t.id === requested)
    ? (requested as string)
    : "archives";
  const [runsKey, setRunsKey] = useState(0);
  const [runs, setRuns] = useState<RunsState>({ kind: "loading" });
  const [config, setConfig] = useState<BackupConfig>();
  const [starting, setStarting] = useState(false);
  const [ownSlug, setOwnSlug] = useState("");

  useEffect(() => {
    document.title = "Backups · Stackstead";
  }, []);

  // Archives are tagged with the identity slug, which can differ from the URL slug a peer is opened under.
  useEffect(() => {
    api.self().then(
      (self) => setOwnSlug(self.slug),
      () => undefined,
    );
  }, [api]);

  // A failed read replaces the runs: the headline must not describe a run it can no longer see.
  useEffect(() => {
    let cancelled = false;
    api.runs().then(
      (list) => {
        if (!cancelled) setRuns({ kind: "ready", runs: list });
      },
      (e: unknown) => {
        if (!cancelled) setRuns({ kind: "error", message: messageOf(e) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [api, runsKey, opsTick]);

  const loadConfig = useCallback(() => {
    api.config().then(setConfig, () => undefined);
  }, [api]);
  useEffect(loadConfig, [loadConfig]);

  async function runNow() {
    setStarting(true);
    try {
      await api.run();
      toasts.push({ kind: "info", message: "Backup started." });
      setRunsKey((k) => k + 1);
    } catch (e) {
      toasts.push({ kind: "danger", message: messageOf(e) });
    } finally {
      setStarting(false);
    }
  }

  const headline = backupHeadline(runs, config, new Date());

  return (
    <section className={styles.page} aria-labelledby="backups-heading">
      <header className={styles.head}>
        <div className={styles.headText}>
          <h1
            id="backups-heading"
            className={styles.title}
            data-tone={headline?.tone}
          >
            {headline?.title ?? "Backups"}
          </h1>
          {headline && <p className={styles.sub}>{headline.sub}</p>}
        </div>
        <Button
          variant="primary"
          loading={starting}
          onClick={() => void runNow()}
        >
          Back up now
        </Button>
      </header>
      <Tabs
        ariaLabel="Backups"
        tabs={TABS}
        selected={tab}
        onSelect={(id) => setQuery({ tab: id })}
      >
        {(selected) =>
          selected === "archives" ? (
            <ArchivesTab api={api} ownSlug={ownSlug} runs={runs} />
          ) : selected === "stacks" ? (
            <BackupStacksTable api={api} runs={runs} />
          ) : (
            <BackupScheduleCard api={api} onSaved={loadConfig} />
          )
        }
      </Tabs>
    </section>
  );
}
