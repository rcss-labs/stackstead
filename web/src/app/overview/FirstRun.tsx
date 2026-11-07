import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { request } from '$lib/api/client';
import type { BackupConfig } from '$lib/api/backupTypes';
import type { RegistryView } from '$lib/api/imageTypes';
import type { HostSnapshot } from '$lib/api/types';
import { useServer } from '../servers/ServerContext';
import styles from './FirstRun.module.css';

export interface FirstRunProps {
  host: HostSnapshot | undefined;
  dockerVersion: string;
}

/** undefined while the lookup has not answered (or failed): the step is then neither done nor claimed to be open. */
type Lookup = boolean | undefined;

/** Whether a backup schedule and a registry exist. This server's own settings, so a connected server skips them. */
function useSetupLookups(enabled: boolean): { backups: Lookup; registry: Lookup } {
  const [state, setState] = useState<{ backups: Lookup; registry: Lookup }>({ backups: undefined, registry: undefined });
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    // Plain requests rather than the settings clients, which would add a chunk for two reads.
    request<BackupConfig>('GET', '/api/backups/config')
      .then(({ data }) => live && setState((s) => ({ ...s, backups: data.schedule.enabled })))
      .catch(() => undefined);
    request<RegistryView[]>('GET', '/api/settings/registries')
      .then(({ data }) => live && setState((s) => ({ ...s, registry: data.length > 0 })))
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [enabled]);
  return state;
}

/** Shown with zero stacks: three ways in, and what is left to set up. */
export function FirstRun({ host, dockerVersion }: FirstRunProps) {
  const { href, local } = useServer();
  const lookups = useSetupLookups(local);

  const unmanaged = host?.containers?.total ?? 0;
  const connected = dockerVersion ? `Docker ${dockerVersion}` : 'Docker';
  const steps: { label: string; to: string | null; done: Lookup }[] = [
    // Reaching this screen means a session exists, so the admin account is there.
    { label: 'Create admin account', to: null, done: true },
    { label: 'Add your first stack', to: '/stacks/new', done: false },
    ...(local
      ? [
          { label: 'Schedule backups', to: '/settings/backups', done: lookups.backups },
          { label: 'Connect a registry', to: '/settings/registries', done: lookups.registry },
        ]
      : []),
  ];
  const doneCount = steps.filter((s) => s.done === true).length;

  const ways = [
    { title: 'Import a compose file', body: 'Bring in a compose file you already have, by its path on this server.', label: 'Import stack', to: '/stacks/import', primary: false },
    { title: 'Create a new stack', body: 'Describe the services you want and bring them up.', label: 'New stack', to: '/stacks/new', primary: true },
    ...(local ? [{ title: 'Restore from a backup', body: 'Bring back one stack from an archive.', label: 'Open backups', to: '/backups', primary: false }] : []),
  ];

  return (
    <div className={styles.page}>
      <div>
        <h1 className={styles.title}>Welcome. Let’s get your first stack in view.</h1>
        <p className={styles.sub}>
          Stackstead is connected to {connected}.
          {unmanaged > 0 && ` Docker has ${unmanaged} ${unmanaged === 1 ? 'container' : 'containers'} that no stack manages.`}
        </p>
      </div>
      <div className={styles.ways}>
        {ways.map((way) => (
          <section key={way.title} className={styles.way}>
            <h2 className={styles.wayTitle}>{way.title}</h2>
            <p className={styles.wayBody}>{way.body}</p>
            <Link to={href(way.to)} className={styles.button} data-primary={way.primary ? 'true' : undefined}>
              {way.label}
            </Link>
          </section>
        ))}
      </div>
      <section className={styles.checklist}>
        <div className={styles.checkHead}>
          <h2 className={styles.wayTitle}>Finish setting up</h2>
          <span className={styles.progress}>
            {doneCount} of {steps.length} done
          </span>
        </div>
        <ul aria-label="Finish setting up" className={styles.steps}>
          {steps.map((step) => {
            const state = step.done === undefined ? 'unknown' : String(step.done);
            const body = (
              <>
                <span className={styles.mark} data-done={state} aria-hidden="true" />
                <span className={styles.stepLabel}>{step.label}</span>
                {step.done === true && <span className={styles.doneWord}>Done</span>}
              </>
            );
            return (
              <li key={step.label} data-done={state}>
                {step.to && step.done !== true ? (
                  <Link to={href(step.to)} className={styles.step}>
                    {body}
                  </Link>
                ) : (
                  <div className={styles.step}>{body}</div>
                )}
              </li>
            );
          })}
        </ul>
      </section>
    </div>
  );
}
