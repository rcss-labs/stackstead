<!-- Test-only: owns the form as reactive state, like a real parent would. -->
<script lang="ts">
  import { createRawSnippet } from 'svelte';
  import ServiceForm from './ServiceForm.svelte';
  import { emptyForm, type FormErrors, type ServiceForm as Form } from '../compose/form';

  let {
    initial,
    errors = {},
    onbrowse,
    onRawEditor,
    withPicker = false,
    showName = false,
    nameReadonly = false,
  }: {
    initial?: Partial<Form>;
    errors?: FormErrors;
    onbrowse?: (mode: 'dir' | 'file', cb: (path: string) => void) => void;
    onRawEditor?: () => void;
    withPicker?: boolean;
    showName?: boolean;
    nameReadonly?: boolean;
  } = $props();

  // svelte-ignore state_referenced_locally
  let form = $state<Form>({ ...emptyForm(), ...initial });
  let name = $state('web');
  let inner: ServiceForm | undefined;

  const picker = createRawSnippet((f: () => Form, errs: () => Record<number, string>) => ({
    render: () => `<div data-testid="picker">${f().networks.length} networks ${JSON.stringify(errs())}</div>`,
  }));

  export const getForm = () => form;
  export const getName = () => name;
  export const focusFirstError = () => inner?.focusFirstError();
</script>

<ServiceForm
  bind:this={inner}
  bind:form
  bind:name
  {errors}
  {onbrowse}
  {onRawEditor}
  {showName}
  {nameReadonly}
  networkPicker={withPicker ? picker : undefined}
/>
