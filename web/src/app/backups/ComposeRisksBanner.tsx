import type { BackupComposeRisk } from '$lib/api/backupTypes';
import { Banner } from '../components/Banner';
import styles from './ComposeRisksBanner.module.css';

/**
 * The host-reaching settings found in the compose files of an archive, grouped by stack. They run as root through
 * Docker once the stack starts, so they are shown before the operator confirms or ticks "start".
 */
export function ComposeRisksBanner({ risks }: { risks: BackupComposeRisk[] }) {
  if (risks.length === 0) return null;
  const byStack = new Map<string, BackupComposeRisk[]>();
  for (const risk of risks) byStack.set(risk.stack, [...(byStack.get(risk.stack) ?? []), risk]);
  return (
    <Banner kind="danger" title="Privileged settings in the restored compose files">
      {[...byStack].map(([stack, list]) => (
        <section key={stack} className={styles.group}>
          <h3 className={styles.stack}>{stack}</h3>
          <ul className={styles.list}>
            {list.map((r, i) => (
              <li key={i} title={r.file}>
                {[r.service || r.file, r.kind, r.detail].filter(Boolean).join(' · ')}
              </li>
            ))}
          </ul>
        </section>
      ))}
    </Banner>
  );
}
