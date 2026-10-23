import { operationStateLook } from '$lib/stacks/operation';
import { Tag } from '../components/Tag';

/** A version's build status, toned like an operation state. */
export function VersionStatus({ status }: { status: string }) {
  const look = operationStateLook(status);
  return (
    <Tag tone={look.tone} mono={false}>
      {look.label}
    </Tag>
  );
}
