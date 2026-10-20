import { emptyForm, fromForm, type ServiceForm } from '../compose/form';
import { serviceConfigYaml } from './composeText';

export interface PreviewCard {
  name: string;
  form: ServiceForm;
}

/** Compose text for the cards as typed. Never throws: a new form has no read-only fields. */
export function previewYaml(cards: PreviewCard[]): string {
  const lines = ['services:'];
  cards.forEach((card, i) => {
    const block = serviceConfigYaml(card.name || `service-${i + 1}`, fromForm(card.form, null)).split('\n');
    if (block.length === 1) block[0] += ' {}';
    for (const line of block) lines.push(`  ${line}`);
  });
  return lines.join('\n');
}

export interface CreateDirtyInput {
  stackName: string;
  cards: { name: string; autoName: boolean; form: ServiceForm }[];
}

const withoutReadOnly = ({ readOnly: _readOnly, ...rest }: ServiceForm) => JSON.stringify(rest);

/** True once the user has changed anything from the untouched New stack screen. */
export function isCreateDirty({ stackName, cards }: CreateDirtyInput): boolean {
  if (stackName !== '' || cards.length > 1) return true;
  const blank = withoutReadOnly(emptyForm());
  return cards.some((c) => (!c.autoName && c.name !== '') || withoutReadOnly(c.form) !== blank);
}
