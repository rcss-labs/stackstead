import { describe, expect, it } from 'vitest';
import { emptyForm } from '../compose/form';
import { isCreateDirty, previewStack } from './createPreview';

describe('previewStack', () => {
  it('renders the cards as the compose document with the card and stack extras', () => {
    const text = previewStack(
      [{ name: 'web', form: { ...emptyForm(), image: 'nginx:1.27' }, extra: { labels: { a: 'b' } } }],
      { volumes: { data: null } },
    );
    expect(text).toContain('services:\n  web:\n    image: nginx:1.27');
    expect(text).toContain('labels:');
    expect(text).toContain('volumes:\n  data:');
  });

  it('falls back to service-<n> for an unnamed card and never throws for a Dockerfile card', () => {
    expect(previewStack([{ name: '', form: emptyForm(), extra: {} }], {})).toContain('service-1: {}');
    const text = previewStack([{ name: '', form: { ...emptyForm(), source: 'dockerfile', dockerfileText: 'FROM x' }, extra: {} }], {});
    expect(text).toContain('./service-1');
  });
});

describe('isCreateDirty', () => {
  const clean = () => ({ stackName: '', cards: [{ name: '', autoName: true, form: emptyForm(), extra: {} as Record<string, unknown> }], stackExtra: {} as Record<string, unknown>, yamlEdited: false });

  it('is clean for the initial state', () => {
    expect(isCreateDirty(clean())).toBe(false);
  });

  it('is dirty with a stack name', () => {
    expect(isCreateDirty({ ...clean(), stackName: 'shop' })).toBe(true);
  });

  it('is dirty with a second card', () => {
    const input = clean();
    input.cards.push({ name: '', autoName: true, form: emptyForm(), extra: {} });
    expect(isCreateDirty(input)).toBe(true);
  });

  it('is dirty once a form field changes', () => {
    const input = clean();
    input.cards[0].form.image = 'x';
    expect(isCreateDirty(input)).toBe(true);
  });

  it("is dirty once the first card's name is edited", () => {
    const input = clean();
    input.cards[0] = { ...input.cards[0], autoName: false, name: 'api' };
    expect(isCreateDirty(input)).toBe(true);
  });

  it('stays clean when the first card only mirrors the stack name', () => {
    const input = clean();
    input.cards[0] = { ...input.cards[0], autoName: true, name: 'web' };
    expect(isCreateDirty(input)).toBe(false);
  });

  it('is dirty with stack extras, a card extra or edited YAML text', () => {
    expect(isCreateDirty({ ...clean(), stackExtra: { volumes: {} } })).toBe(true);
    const withCardExtra = clean();
    withCardExtra.cards[0].extra = { labels: {} };
    expect(isCreateDirty(withCardExtra)).toBe(true);
    expect(isCreateDirty({ ...clean(), yamlEdited: true })).toBe(true);
  });

  it('is dirty with no cards at all', () => {
    expect(isCreateDirty({ ...clean(), cards: [] })).toBe(true);
  });
});
