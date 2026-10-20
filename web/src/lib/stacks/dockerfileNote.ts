const OUTSIDE_REASONS = ['outside the stack folder', 'outside the allowed directories'];
export const OUTSIDE_NOTE = "This Dockerfile is outside the stack's folder and can't be edited here.";

/** The sentence explaining why a Dockerfile cannot be edited; shared by the stack page and Edit service. */
export function dockerfileNote(reason: string | undefined): string {
  return OUTSIDE_REASONS.includes(reason ?? '') ? OUTSIDE_NOTE : `This Dockerfile can't be edited here: ${reason ?? 'not editable'}`;
}
