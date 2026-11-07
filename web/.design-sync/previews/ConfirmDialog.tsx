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

export function TypedConfirm() {
  return (
    <ConfirmDialog
      open
      danger
      title="Remove stack and volumes?"
      message="This deletes the stack's volumes and their data. It cannot be undone."
      confirmLabel="Remove stack"
      typedConfirm="monitoring"
      onConfirm={() => {}}
      onCancel={() => {}}
    />
  );
}
