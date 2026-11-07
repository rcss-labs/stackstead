import type { RawCompose, ServiceDetail } from '$lib/api/types';
import { dedent, serviceBlock, serviceConfigYaml } from '$lib/stacks/composeText';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { PanelLabel } from '../components/PanelLabel';
import { Spinner } from '../components/Spinner';
import styles from './ConfigTab.module.css';
import { dependsOn, neededBy } from './dependsOn';

export interface ConfigTabProps {
  service: ServiceDetail;
  compose: RawCompose | undefined;
  error: string;
  onRetry: () => void;
  onEditYaml: () => void;
  /** Every service of the stack, to work out which ones need this one. */
  services?: string[];
}

export function ConfigTab({ service, compose, error, onRetry, onEditYaml, services = [] }: ConfigTabProps) {
  if (compose === undefined) {
    return (
      <div className={styles.tab}>
        {error ? (
          <>
            <Banner kind="danger" title="Could not load the compose file">
              {error}
            </Banner>
            <p>
              <Button variant="secondary" onClick={onRetry}>
                Retry
              </Button>
            </p>
          </>
        ) : (
          <Spinner />
        )}
      </div>
    );
  }
  const block = serviceBlock(compose.content, service.name);
  const list = (names: string[], none: string) => (names.length > 0 ? names.join(', ') : none);
  const { networks, networkMode } = service.config;
  const networkText = networks?.length ? networks.map((n) => n.name).join(', ') : networkMode ? `network_mode: ${networkMode}` : 'Default';
  return (
    <div className={styles.tab}>
      <pre className={styles.yaml}>{block !== undefined ? dedent(block) : serviceConfigYaml(service.name, service.config)}</pre>
      {block === undefined && <p className={styles.note}>Shown from the parsed configuration; the file&apos;s own text could not be isolated.</p>}
      <section className={styles.used} aria-label="Used by">
        <PanelLabel>Used by</PanelLabel>
        <dl className={styles.kv}>
          <dt>Networks</dt>
          <dd>{networkText}</dd>
          <dt>Depends on</dt>
          <dd>{list(dependsOn(compose.content, service.name), 'Nothing')}</dd>
          <dt>Needed by</dt>
          <dd>{list(neededBy(compose.content, service.name, services), 'Nothing')}</dd>
        </dl>
      </section>
      <div>
        <Button variant="secondary" size="sm" onClick={onEditYaml}>
          Edit YAML
        </Button>
      </div>
    </div>
  );
}
