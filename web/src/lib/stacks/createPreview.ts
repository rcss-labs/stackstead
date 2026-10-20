import { emptyForm, type ServiceForm } from '../compose/form';
import { stackToYaml, type Extra, type StackCard } from '../compose/stackYaml';

/** The compose.yaml Create would write. An unnamed card gets a placeholder key so the preview never fails. */
export function previewStack(cards: StackCard[], stackExtra: Extra): string {
  return stackToYaml(
    cards.map((card, i) => ({ ...card, name: card.name || `service-${i + 1}` })),
    stackExtra,
  );
}

export interface CreateDirtyInput {
  stackName: string;
  cards: { name: string; autoName: boolean; form: ServiceForm; extra: Extra }[];
  stackExtra: Extra;
  /** The YAML text was typed into since it was last generated from the cards. */
  yamlEdited: boolean;
}

const withoutReadOnly = ({ readOnly: _readOnly, ...rest }: ServiceForm) => JSON.stringify(rest);

/** True once the user has changed anything from the untouched New stack screen. */
export function isCreateDirty({ stackName, cards, stackExtra, yamlEdited }: CreateDirtyInput): boolean {
  if (stackName !== '' || cards.length !== 1 || yamlEdited || Object.keys(stackExtra).length > 0) return true;
  const blank = withoutReadOnly(emptyForm());
  return cards.some((c) => Object.keys(c.extra).length > 0 || (!c.autoName && c.name !== '') || withoutReadOnly(c.form) !== blank);
}
