import { ConfirmDialog } from 'stackstead-ui';

export function DangerConfirm() {
  return (
    <ConfirmDialog
      open
      danger
      title="Remove stack monitoring?"
      message="This stops and removes 4 containers and the stack's networks. Named volumes are kept."
      confirmLabel="Remove stack"
      onConfirm={() => {}}
      onCancel={() => {}}
    />
  );
}
