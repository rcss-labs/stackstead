import { useId } from 'react';
import { Link } from 'react-router';
import type { IssueAction, IssueCard as IssueCardData } from '$lib/dashboard/issues';
import { Button } from '../components/Button';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { RelativeTime } from '../components/RelativeTime';
import styles from './IssueCard.module.css';

export interface IssueCardProps {
  card: IssueCardData;
  /** Why the card's lifecycle action is unavailable, or undefined. */
  disabledReason?: string;
  /** An action request for the card's stack is in flight. */
  acting: boolean;
  onAct: (stack: string, action: 'up' | 'restart') => void;
}

const KIND_TONE = { danger: 'danger', warning: 'warning', info: 'info' } as const;

export function IssueCard({ card, disabledReason, acting, onAct }: IssueCardProps) {
  const titleId = useId();

  function renderAction(action: IssueAction, variant: 'primary' | 'secondary') {
    if (action.kind === 'act') {
      return (
        <Button variant="primary" size="sm" disabledReason={disabledReason} loading={acting} onClick={() => onAct(action.stack, action.action)}>
          {action.label}
        </Button>
      );
    }
    return (
      <Link to={action.href} className={styles.action} data-variant={variant}>
        {action.label}
      </Link>
    );
  }

  return (
    <article className={styles.card} aria-labelledby={titleId} data-tone={card.tone}>
      <div className={styles.head}>
        <Led tone={card.tone} pulse={card.pulse} />
        <PanelLabel as="span" tone={KIND_TONE[card.tone]}>
          {card.kind.replace(/_/g, ' ')}
        </PanelLabel>
        <span className="visually-hidden">{card.tone}</span>
        {card.where && (
          <Link to={card.where.href} className={styles.where} data-mono={card.where.mono ? 'true' : undefined}>
            {card.where.label}
          </Link>
        )}
        {card.since && (
          <span className={styles.since}>
            <RelativeTime iso={card.since} />
          </span>
        )}
      </div>
      <h3 id={titleId} className={styles.title}>
        {card.title}
      </h3>
      {card.message && card.message !== card.title && <p className={styles.message}>{card.message}</p>}
      {(card.primary || card.secondary) && (
        <div className={styles.actions}>
          {card.primary && renderAction(card.primary, 'primary')}
          {card.secondary && renderAction(card.secondary, 'secondary')}
        </div>
      )}
    </article>
  );
}
