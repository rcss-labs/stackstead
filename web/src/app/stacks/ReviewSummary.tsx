import { composeFileName } from '$lib/stacks/composeText';
import { Banner } from '../components/Banner';
import { changedLineCount } from './changedLineCount';

export interface ReviewSummaryProps {
  original: string;
  modified: string;
  /** Path of the compose file; names the file and its .bak. */
  path?: string;
}

/** States the size of the change above the diff, before the user reads it. */
export function ReviewSummary({ original, modified, path }: ReviewSummaryProps) {
  const fileName = composeFileName(path);
  const count = changedLineCount(original, modified);
  if (count === 0) return <Banner kind="info" title={`No lines change in ${fileName}`} />;
  return (
    <Banner kind="info" title={count === 1 ? `One line changes in ${fileName}` : `${count} lines change in ${fileName}`}>
      Your comments and key order are kept. The previous version is saved as <code>{`${fileName}.bak`}</code>.
    </Banner>
  );
}
