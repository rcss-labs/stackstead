import styles from './ComposePreview.module.css';

/** The compose.yaml that Create would write, updated as the form is typed. */
export function ComposePreview({ text }: { text: string }) {
  return (
    <section className={styles.preview} aria-labelledby="preview-heading">
      <div className={styles.header}>
        <h2 id="preview-heading">compose.yaml</h2>
        <span className={styles.newFile}>new file</span>
        <span className={styles.spacer} />
        <span className={styles.live}>live preview</span>
      </div>
      <pre className={styles.code}>{text}</pre>
    </section>
  );
}
