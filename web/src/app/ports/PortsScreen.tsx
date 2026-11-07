import { useMemo, useState } from "react";
import { Link } from "react-router";
import { nextFreePort, portRows, type PortRow } from "$lib/ports/ports";
import { Banner } from "../components/Banner";
import { EmptyState } from "../components/EmptyState";
import { StatusBadge } from "../components/StatusBadge";
import { Tag } from "../components/Tag";
import { useContainerList } from "../containers/useContainerList";
import { useStore } from "../hooks/useStore";
import { useServer } from "../servers/ServerContext";
import styles from "./PortsScreen.module.css";

const label = (r: PortRow) =>
  r.last > r.port ? `${r.port}-${r.last}` : String(r.port);
const SEARCH_FROM = 8000;

export function PortsScreen() {
  const { stacks, href, local, name } = useServer();
  const {
    items,
    loading: stacksLoading,
    error: stacksError,
  } = useStore(stacks);
  const { rows: containers, error: containersError } = useContainerList();
  const [query, setQuery] = useState("");

  const rows = useMemo(
    () => portRows(items, containers ?? []),
    [items, containers],
  );
  const q = query.trim().toLowerCase();
  const shown = q
    ? rows.filter((r) =>
        [label(r), r.stack, r.service, r.container].some((v) =>
          v?.toLowerCase().includes(q),
        ),
      )
    : rows;
  // Rows are complete only when both lists loaded; otherwise a port in use could look free or absent.
  const known =
    !stacksLoading &&
    !stacksError &&
    containers !== undefined &&
    !containersError;
  const free = known ? nextFreePort(rows, SEARCH_FROM) : undefined;

  return (
    <div className={styles.page}>
      <header>
        <h1 className={styles.title}>Ports</h1>
        <p className={styles.sub}>
          Host ports published on {local ? "this server" : name}, from stack
          files and running containers.
        </p>
      </header>

      {stacksError && (
        <Banner kind="warning" title="Could not read stacks">
          {stacksError}. Only running containers are listed.
        </Banner>
      )}
      {containersError && (
        <Banner kind="warning" title="Could not read running containers">
          {containersError}. Only ports declared by stacks are listed.
        </Banner>
      )}

      <div className={styles.bar}>
        <input
          type="search"
          className={styles.search}
          aria-label="Filter ports"
          placeholder="Port, stack or service"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className={styles.next}>
          Next port above {SEARCH_FROM} not used by Docker:{" "}
          <b className={styles.mono}>
            {free === undefined ? "unknown" : free || "none"}
          </b>
        </span>
      </div>

      {shown.length === 0 && rows.length === 0 && !known ? (
        <EmptyState
          title="Ports not known yet"
          description={
            stacksError || containersError
              ? "Some sources could not be read, so published ports are unknown."
              : "Still reading stacks and containers."
          }
        />
      ) : shown.length === 0 ? (
        <EmptyState
          title={rows.length === 0 ? "No published ports" : "No match"}
          description={
            rows.length === 0
              ? "Nothing here publishes a host port."
              : "No port, stack or service matches that filter."
          }
        />
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Port</th>
              <th>Service</th>
              <th>Stack</th>
              <th>Protocol</th>
              <th>Bound to</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r, i) => (
              <tr
                key={`${r.port}-${r.container ?? r.stack}-${r.proto ?? ""}-${r.bind ?? ""}-${i}`}
              >
                <td className={styles.mono} data-label="Port">
                  :{label(r)}
                </td>
                <td className={styles.mono} data-label="Service">
                  {r.container ? (
                    <Link
                      to={href(
                        `/containers/${encodeURIComponent(r.container)}`,
                      )}
                    >
                      {r.container}
                    </Link>
                  ) : (
                    (r.service ?? "—")
                  )}
                </td>
                <td data-label="Stack">
                  {r.stack ? (
                    <Link to={href(`/stacks/${encodeURIComponent(r.stack)}`)}>
                      <Tag>{r.stack}</Tag>
                    </Link>
                  ) : (
                    <span className={styles.muted}>not in a stack</span>
                  )}
                </td>
                <td className={styles.muted} data-label="Protocol">
                  {r.proto ?? "—"}
                </td>
                <td
                  className={`${styles.mono} ${styles.muted}`}
                  data-label="Bound to"
                >
                  {r.bind ?? "—"}
                </td>
                <td data-label="Status">
                  <StatusBadge status={r.state} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export default PortsScreen;
