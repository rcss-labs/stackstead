import { Button, EmptyState } from 'stackstead-ui';

export function WithAction() {
  return (
    <EmptyState title="No stacks yet" description="Create a stack from a compose file or a Git repository.">
      <Button variant="primary">Create stack</Button>
    </EmptyState>
  );
}

export function TitleAndDescription() {
  return <EmptyState title="No volumes" description="Volumes created by your stacks will show up here." />;
}
