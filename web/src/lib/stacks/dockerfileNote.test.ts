import { describe, expect, it } from 'vitest';
import { dockerfileNote } from './dockerfileNote';

describe('dockerfileNote', () => {
  it.each(['outside the stack folder', 'outside the allowed directories'])('uses the outside note for %s', (reason) => {
    expect(dockerfileNote(reason)).toBe("This Dockerfile is outside the stack's folder and can't be edited here.");
  });
  it('explains any other reason', () => {
    expect(dockerfileNote('uses variables')).toBe("This Dockerfile can't be edited here: uses variables");
    expect(dockerfileNote(undefined)).toBe("This Dockerfile can't be edited here: not editable");
  });
});
