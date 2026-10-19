import type { ReactNode } from 'react';
import type { ServiceDetail } from '$lib/api/types';
import type { ServiceTab } from '$lib/stacks/detailLocation';
import { serviceState } from '$lib/stacks/summary';
import { Button } from '../components/Button';
import { Led } from '../components/Led';
import { Tabs } from '../components/Tabs';
import styles from './ServicePane.module.css';
import { ServiceOverview } from './ServiceOverview';

export interface ServicePaneProps {
  service: ServiceDetail;
  tab: ServiceTab;
  onTab: (tab: ServiceTab) => void;
  onLogs: () => void;
  latestCpu: number | undefined;
  cpuHour: (number | null)[] | undefined;
  /** Shows Edit (or Close editor while `editor` is open) when given. */
  onEdit?: () => void;
  onCloseEditor?: () => void;
  /** The service editor; while present it replaces the tabs and their panel. */
  editor?: ReactNode;
  /** Content for the Metrics and Config tabs (a later plan fills it). */
  children?: (tab: ServiceTab) => ReactNode;
}

const TABS: { id: ServiceTab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'metrics', label: 'Metrics' },
  { id: 'config', label: 'Config' },
];

export function ServicePane({ service, tab, onTab, onLogs, latestCpu, cpuHour, onEdit, onCloseEditor, editor, children }: ServicePaneProps) {
  const state = serviceState(service);
  return (
    <div className={styles.pane}>
      <header className={styles.header}>
        <Led tone={state.tone} pulse={state.tone === 'danger'} size={8} />
        <h2 className={styles.name}>{service.name}</h2>
        <span className={styles.state} data-tone={state.tone}>
          {state.text}
        </span>
        <span className={styles.spacer} />
        <Button variant="secondary" size="sm" aria-label={`Logs for ${service.name}`} onClick={onLogs}>
          Logs
        </Button>
        {editor ? (
          <Button variant="ghost" size="sm" onClick={onCloseEditor}>
            Close editor
          </Button>
        ) : (
          onEdit && (
            <Button variant="secondary" size="sm" aria-label={`Edit ${service.name}`} onClick={onEdit}>
              Edit
            </Button>
          )
        )}
      </header>
      {editor ? (
        editor
      ) : (
        <div className={styles.tabs}>
          <Tabs tabs={TABS} selected={tab} onSelect={(id) => onTab(id as ServiceTab)} ariaLabel="Service sections" activation="manual">
            {(selected) =>
              selected === 'overview' ? (
                <ServiceOverview service={service} latestCpu={latestCpu} cpuHour={cpuHour} />
              ) : (
                children?.(selected as ServiceTab)
              )
            }
          </Tabs>
        </div>
      )}
    </div>
  );
}
