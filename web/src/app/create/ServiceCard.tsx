import type { Ref } from 'react';
import type { ServiceForm as ServiceFormValue } from '$lib/compose/form';
import { Button } from '../components/Button';
import { NetworkPicker } from '../stacks/NetworkPicker';
import { ServiceForm } from '../stacks/ServiceForm';
import type { BrowsePurpose, PortNote, ServiceFormHandle } from '../stacks/ServiceForm';
import styles from './ServiceCard.module.css';
import type { Card } from './useCreateForm';

export interface ServiceCardProps {
  card: Card;
  index: number;
  open: boolean;
  removable: boolean;
  /** Pending or browsing: the header and Remove are disabled. */
  frozen: boolean;
  /** Pending alone disables the form fields. */
  pending: boolean;
  errorCount: number;
  onOpen: () => void;
  onRemove: () => void;
  onNameChange: (name: string) => void;
  onFormChange: (form: ServiceFormValue) => void;
  onBrowse: (mode: 'dir' | 'file', set: (path: string) => void, purpose: BrowsePurpose) => void;
  portNote?: (spec: string, index: number) => PortNote | undefined;
  formRef: Ref<ServiceFormHandle>;
}

export function ServiceCard({ card, index, open, removable, frozen, pending, errorCount, onOpen, onRemove, onNameChange, onFormChange, onBrowse, portNote, formRef }: ServiceCardProps) {
  const title = card.name || `Service ${index + 1}`;
  return (
    <section className={styles.card} aria-labelledby={`service-heading-${card.id}`}>
      <div className={styles.header}>
        <h2>
          <button id={`service-heading-${card.id}`} type="button" className={styles.toggle} aria-expanded={open} aria-controls={`service-body-${card.id}`} disabled={frozen} onClick={onOpen}>
            <span className={styles.title}>{title}</span>{' '}
            <span className={styles.summary}>{card.form.source === 'build' ? `build: ${card.form.buildContext}` : card.form.image}</span>{' '}
            {!open && errorCount > 0 && (
              <span className={styles.errorMarker}>
                <span aria-hidden="true">⚠</span> {errorCount} {errorCount === 1 ? 'error' : 'errors'}
              </span>
            )}
          </button>
        </h2>
        {removable && (
          <Button variant="ghost" icon aria-label={`Remove service ${title}`} disabled={frozen} onClick={onRemove}>
            ✕
          </Button>
        )}
      </div>
      <div id={`service-body-${card.id}`} hidden={!open} className={styles.body}>
        {open && (
          <fieldset className={styles.fieldset} disabled={pending}>
            <ServiceForm
              ref={formRef}
              form={card.form}
              onChange={onFormChange}
              errors={card.errors}
              showName
              name={card.name}
              onNameChange={onNameChange}
              idPrefix={`svc-${card.id}`}
              onBrowse={onBrowse}
              advanced={{ summary: 'Environment, networks, limits, healthcheck' }}
              portNote={portNote}
              networkPicker={(f, rowErrors) => (
                <NetworkPicker idPrefix={`svc-${card.id}-networks`} value={f.networks} onChange={(networks) => onFormChange({ ...f, networks })} errors={rowErrors} />
              )}
            />
          </fieldset>
        )}
      </div>
    </section>
  );
}
