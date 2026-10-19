import type { RawCompose, ServiceDetail } from '$lib/api/types';
import { serviceBlock, serviceConfigYaml } from '$lib/stacks/composeText';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Spinner } from '../components/Spinner';
import styles from './ConfigTab.module.css';

export interface ConfigTabProps {
  service: ServiceDetail;
  compose: RawCompose | undefined;
  error: string;
  onRetry: () => void;
  onEditYaml: () => void;
}

export function ConfigTab({ service, compose, error, onRetry, onEditYaml }: ConfigTabProps) {
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
  return (
    <div className={styles.tab}>
      <pre className={styles.yaml}>{block ?? serviceConfigYaml(service.name, service.config)}</pre>
      {block === undefined && <p className={styles.note}>The {service.name} block was not found in the file as written; showing the formatted configuration.</p>}
      <div>
        <Button variant="secondary" size="sm" onClick={onEditYaml}>
          Edit YAML
        </Button>
      </div>
    </div>
  );
}
