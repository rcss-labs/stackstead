/** Human summary of a retention policy; 0 on either rule means that rule is off. */
export function retentionText(r: { keepLast: number; keepDays: number }): string {
  if (r.keepLast > 0 && r.keepDays > 0) {
    return `Keep the last ${r.keepLast} and anything from the last ${r.keepDays} days`;
  }
  if (r.keepLast > 0) return `Keep the last ${r.keepLast}`;
  if (r.keepDays > 0) return `Keep ${r.keepDays} days`;
  return 'Keep forever';
}
