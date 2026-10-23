/** Image operations are keyed `image/<owner-id>/<slug>`; stack operations carry the stack name. */
export function operationSubject(op: { stack: string }): { label: string; href: string } {
  const parts = op.stack.split('/');
  if (parts.length === 3 && parts[0] === 'image') {
    const slug = parts[2];
    return { label: `image ${slug}`, href: `/images/${slug}` };
  }
  return { label: op.stack, href: `/stacks/${op.stack}` };
}
