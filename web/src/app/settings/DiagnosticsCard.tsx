import { useEffect, useState } from 'react';
import { api } from '$lib/api/client';
import { buildDiagnostics } from '$lib/settings/diagnostics';
import { live } from '$lib/stores/live';
import { CopyButton } from '../components/CopyButton';
import { PanelLabel } from '../components/PanelLabel';
import { useStore } from '../hooks/useStore';
import { SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';

/** Version details to paste into a support request. */
export function DiagnosticsCard() {
  const liveState = useStore(live);
  const [version, setVersion] = useState('');

  useEffect(() => {
    let current = true;
    api.serverSelf().then(
      (self) => current && setVersion(self.version),
      () => {}, // the copy then says "unknown version"
    );
    return () => {
      current = false;
    };
  }, []);

  return (
    <SettingsCard id="diagnostics-h" title="This server" footer="Version and status only. Tokens, passwords and addresses are never included.">
      <dl className={styles.status}>
        <div>
          <dt>
            <PanelLabel as="span">Version</PanelLabel>
          </dt>
          <dd className={styles.value}>{version || 'unknown'}</dd>
        </div>
      </dl>
      <div>
        <CopyButton text={buildDiagnostics(version, liveState)} label="Copy diagnostics" variant="ghost" size="sm" />
      </div>
    </SettingsCard>
  );
}
