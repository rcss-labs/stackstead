import type { BackupRestorePlan } from '$lib/api/backupTypes';
import { formatBytes } from '$lib/format';
import { Banner } from '../components/Banner';
import { Tag } from '../components/Tag';
import { DiffView } from '../stacks/DiffView';
import { ComposeRisksBanner } from './ComposeRisksBanner';
import styles from './backups.module.css';

/** What a restore would do, from the server's plan. */
export function RestorePreview({ plan, replacesVolumes }: { plan: BackupRestorePlan; replacesVolumes: boolean }) {
  return (
    <div className={styles.preview} aria-label="Restore preview" role="region">
      <div className={styles.previewHead}>
        <strong>
          Restores to <span className={styles.mono}>{plan.targetDir}</span>
        </strong>
        {plan.crossHost && <Tag tone="warning">Cross-host</Tag>}
        {plan.encrypted ? (
          <Tag tone="success" mono={false}>{`Encrypted · key ${plan.keyId}`}</Tag>
        ) : (
          <Tag tone="warning" mono={false}>
            Unencrypted
          </Tag>
        )}
        {plan.overwrites && <Tag tone="danger">Overwrites existing stack</Tag>}
      </div>

      <ComposeRisksBanner risks={plan.risks} />

      {plan.warnings.length > 0 && (
        <Banner kind="warning" title="Warnings">
          <ul className={styles.warnings}>
            {plan.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </Banner>
      )}

      {plan.fileDiffs.map((d) => (
        <div key={d.path}>
          <h3 className={styles.groupTitle}>{d.path}</h3>
          <DiffView original={d.old} modified={d.new} ariaLabel={`Changes to ${d.path}`} />
        </div>
      ))}

      {plan.volumes.length > 0 && (
        <div>
          <h3 className={styles.groupTitle}>Volumes</h3>
          {replacesVolumes && (
            <Banner kind="warning" title="Volume contents are replaced">
              Restored volumes lose their current contents.
            </Banner>
          )}
          <table className={styles.table}>
            <thead>
              <tr>
                <th scope="col">Volume</th>
                <th scope="col">Mounted at</th>
                <th scope="col">Size</th>
                <th scope="col">Result</th>
              </tr>
            </thead>
            <tbody>
              {plan.volumes.map((v) => (
                <tr key={v.key}>
                  <td className={styles.mono}>{v.name}</td>
                  <td className={styles.mono}>{v.target}</td>
                  <td>{formatBytes(v.bytes)}</td>
                  <td>{v.skip ? <span className={styles.skipReason}>{v.reason}</span> : 'Restored'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {plan.images.length > 0 && (
        <div>
          <h3 className={styles.groupTitle}>Images</h3>
          <ul className={styles.mounts}>
            {plan.images.map((image) => (
              <li key={image} className={styles.mono}>
                {image}
              </li>
            ))}
          </ul>
        </div>
      )}

      {plan.missingNetworks.length > 0 && (
        <div>
          <h3 className={styles.groupTitle}>Missing networks</h3>
          <ul className={styles.mounts}>
            {plan.missingNetworks.map((n) => (
              <li key={n.name}>
                <span className={styles.mono}>{n.name}</span>
                <span className={styles.muted}>{n.driver}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
