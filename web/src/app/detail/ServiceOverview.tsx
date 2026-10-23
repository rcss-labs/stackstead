import { Link } from 'react-router';
import type { ServiceDetail } from '$lib/api/types';
import { formatPercent } from '$lib/format';
import { serviceState } from '$lib/stacks/summary';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { Sparkline } from '../components/Sparkline';
import { Tag } from '../components/Tag';
import styles from './ServiceOverview.module.css';

import { useServer } from '../servers/ServerContext';
export interface ServiceOverviewProps {
  service: ServiceDetail;
  latestCpu: number | undefined;
  cpuHour: (number | null)[] | undefined;
}

/** "8123:80" as ":8123 → 80"; a bare container port is shown as is. */
function portChip(port: string): string {
  const [host, ...container] = port.split(':');
  return container.length === 0 ? port : `:${host} → ${container.join(':')}`;
}

function Chips({ values, tone }: { values: string[]; tone?: 'info' }) {
  if (values.length === 0) return <span className={styles.dash}>—</span>;
  return (
    <span className={styles.chips}>
      {values.map((value) => (
        <span key={value} className={styles.chip} data-tone={tone}>
          {value}
        </span>
      ))}
    </span>
  );
}

export function ServiceOverview({ service, latestCpu, cpuHour }: ServiceOverviewProps) {
  const { href } = useServer();
  const { config, library } = service;
  const image = config.image ?? (config.build ? `build: ${config.build.context}` : '');
  const limits = [config.limits.cpus ? `${config.limits.cpus} cpus` : '', config.limits.memory ?? ''].filter(Boolean).join(' · ');
  const text = (value: string) => (value ? value : <span className={styles.dash}>—</span>);
  return (
    <div className={styles.overview}>
      <div className={styles.details}>
        <dl className={styles.dl}>
          <dt>Image</dt>
          <dd>
            {library ? `${library.image}:${library.tag} (${library.ownerName})` : text(image)}
            {library?.newer && (
              <>
                {' '}
                <Tag tone="info">{`new version available: ${library.latestTag}`}</Tag>
              </>
            )}
          </dd>
          <dt>Ports</dt>
          <dd>
            <Chips values={(config.ports ?? []).map(portChip)} tone="info" />
          </dd>
          <dt>Volumes</dt>
          <dd>
            <Chips values={config.volumes ?? []} />
          </dd>
          <dt>Restart</dt>
          <dd>{text(config.restart ?? '')}</dd>
          <dt>Limits</dt>
          <dd>{text(limits)}</dd>
        </dl>
        {service.readOnly.length > 0 && (
          <p className={styles.note}>Read-only in the form: {service.readOnly.join(', ')}. Change these in the Compose file.</p>
        )}
      </div>
      <div className={styles.side}>
        <section className={styles.card}>
          <PanelLabel>Container</PanelLabel>
          {service.containers.length === 0 ? (
            <p className={styles.empty}>No containers.</p>
          ) : (
            <ul className={styles.containers}>
              {service.containers.map((container) => {
                const tone = serviceState({ ...service, containers: [container] }).tone;
                return (
                  <li key={container.id}>
                    <Led tone={tone} size={7} />
                    <div>
                      <Link className={styles.containerName} to={href(`/containers/${encodeURIComponent(container.name)}`)}>
                        {container.name}
                      </Link>
                    </div>
                    <div className={styles.containerStatus}>{container.status}</div>
                    {container.state === 'running' && (
                      <Link className={styles.terminalLink} to={href(`/containers/${encodeURIComponent(container.name)}?tab=terminal`)} aria-label={`Open terminal for ${container.name}`}>
                        Terminal
                      </Link>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </section>
        <section className={styles.card}>
          <div className={styles.head}>
            <PanelLabel>CPU · 1h</PanelLabel>
            <p className={styles.cpuNow}>{latestCpu === undefined ? '—' : formatPercent(latestCpu)}</p>
          </div>
          <Sparkline values={cpuHour ?? []} tone="accent" width={100} height={40} />
        </section>
      </div>
    </div>
  );
}
