import { Link } from 'react-router';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { previewYaml } from '$lib/stacks/createPreview';
import { BROWSE_TITLES } from '../stacks/ServiceForm';
import { FileBrowser } from '../stacks/FileBrowser';
import { MAX_SERVICES, useCreateForm } from './useCreateForm';
import { ServiceCard } from './ServiceCard';
import { ComposePreview } from './ComposePreview';
import { usePortCheck } from './usePortCheck';
import styles from './StackCreateScreen.module.css';

import { useServer } from '../servers/ServerContext';
export function StackCreateScreen() {
  const { href } = useServer();
  const form = useCreateForm();
  const { cards, browser, pending } = form;
  const frozen = pending || browser !== null;
  const portNoteFor = usePortCheck(cards);

  return (
    <section className={styles.page}>
      <div className={styles.main}>
        <header className={styles.header}>
          <h1>New stack</h1>
          <p className={styles.lead}>
            Writes <code>compose.yaml</code> into a folder named <code>{form.stackName || 'my-stack'}</code> in the stacks directory. Nothing is written until you confirm.
          </p>
        </header>
        {form.unavailable && (
          <div id="create-unavailable" tabIndex={-1}>
            <Banner kind="danger" title="Docker/compose unavailable: the stack was not created">
              {form.unavailable}
            </Banner>
          </div>
        )}
        <form id="create-form" className={styles.card} noValidate onSubmit={form.submit}>
          <div className={styles.stackName}>
            <Field
              label="Stack name"
              id="stack-name"
              mono
              hint="Lowercase letters, digits, '-' and '_'; starts with a letter or digit; at most 63 characters."
              error={form.stackNameError}
            >
              {({ readOnly: _readOnly, ...aria }) => (
                <input {...aria} type="text" value={form.stackName} onChange={(e) => form.setStackName(e.target.value)} disabled={pending} autoComplete="off" spellCheck={false} autoCapitalize="off" />
              )}
            </Field>
          </div>
          {form.formError && (
            <div id="create-error" className={styles.formError} role="alert" tabIndex={-1}>
              {form.formError}
            </div>
          )}
          {cards.map((card, index) => (
            <ServiceCard
              key={card.id}
              card={card}
              index={index}
              open={form.openId === card.id}
              removable={cards.length > 1}
              frozen={frozen}
              pending={pending}
              errorCount={form.errorCount(card)}
              onOpen={() => form.open(card.id)}
              onRemove={() => form.removeCard(card.id)}
              onNameChange={(name) => form.setCardName(card.id, name)}
              onFormChange={(next) => form.setCardForm(card.id, next)}
              onBrowse={(mode, set, purpose) => form.browse(card.id, mode, set, purpose)}
              portNote={portNoteFor(card.id)}
              formRef={form.serviceFormRef}
            />
          ))}
          <div className={styles.addRow}>
            <Button
              variant="secondary"
              className={styles.addButton}
              disabled={frozen}
              disabledReason={cards.length >= MAX_SERVICES ? 'A stack can have at most 20 services.' : undefined}
              onClick={form.addCard}
            >
              + Add another service
            </Button>
          </div>
        </form>
      </div>

      <aside className={styles.side}>
        <ComposePreview text={previewYaml(cards)} />
        <label className={styles.check}>
          <input type="checkbox" checked={form.startNow} onChange={(e) => form.setStartNow(e.target.checked)} />
          <span>
            Start now (<code>up --build</code>)
          </span>
        </label>
        <div className={styles.actions}>
          <Link to={href('/')} className={styles.cancel}>
            Cancel
          </Link>
          <Button variant="primary" type="submit" form="create-form" className={styles.create} loading={pending}>
            Create stack
          </Button>
        </div>
      </aside>

      <FileBrowser
        open={browser !== null}
        mode={browser?.mode ?? 'dir'}
        title={BROWSE_TITLES[browser?.purpose ?? 'context']}
        onPick={form.pick}
        onClose={form.closeBrowser}
      />
    </section>
  );
}
