import type { StackStatus } from '$lib/api/types';
import type { IssueCard as IssueCardData } from '$lib/dashboard/issues';
import { IssueCard } from './IssueCard';
import styles from './IssueCards.module.css';

export interface IssueCardsProps {
  /** undefined while alerts are not loaded. */
  cards: IssueCardData[] | undefined;
  stackStatus: (name: string) => StackStatus | undefined;
  reasonFor: (name: string, status: StackStatus | undefined) => string | undefined;
  acting: ReadonlySet<string>;
  onAct: (stack: string, action: 'up' | 'restart') => void;
}

/** What needs attention, derived from alerts and reclaimable storage. Says nothing when there is nothing to say. */
export function IssueCards({ cards, stackStatus, reasonFor, acting, onAct }: IssueCardsProps) {
  if (!cards || cards.length === 0) return null;
  return (
    <section aria-labelledby="overview-issues" className={styles.section}>
      <h2 id="overview-issues" className="visually-hidden">
        Issues
      </h2>
      <div className={styles.grid}>
        {cards.map((card) => {
          const action = card.primary?.kind === 'act' ? card.primary : undefined;
          return (
            <IssueCard
              key={card.id}
              card={card}
              disabledReason={action ? reasonFor(action.stack, stackStatus(action.stack)) : undefined}
              acting={action ? acting.has(action.stack) : false}
              onAct={onAct}
            />
          );
        })}
      </div>
    </section>
  );
}
