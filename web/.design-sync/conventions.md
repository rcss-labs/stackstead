# Stackstead conventions (read first)

Stackstead is a dense, quiet admin UI for Docker stacks: 13px IBM Plex Sans body, Geist Mono for identifiers, light surfaces, one green accent. Components are in `window.Stackstead`.

## Setup

No provider or theme wrapper is needed. Link `styles.css` and the components are styled. It also sets the page `body` (background `var(--bg)`, 13px/1.45 `var(--sans)`, tabular numbers) and a global `.visually-hidden` class. Dark mode follows the OS; force a theme with `<html data-theme="dark">` or `data-theme="light"`. `data-density="comfortable"` on `<html>` widens `--pad`/`--row`.

## Styling idiom: tokens, not classes

Component class names are CSS-module hashes, so never write or target them. Style components through their props (`variant`, `size`, `tone`, `kind`, `mono`). For your own layout glue, use inline styles or your own CSS with these custom properties (all defined in `_ds_bundle.css`, imported by `styles.css`; there is no separate tokens file):

| Family | Tokens |
|---|---|
| Surfaces | `--bg` page, `--chassis`, `--surface` cards, `--raised`, `--hover`, `--overlay` |
| Borders | `--border`, `--border-strong`, `--border-input` |
| Text | `--text`, `--muted`, `--faint` |
| Intent | `--accent`, `--accent-ink`, `--accent-soft`, `--success`, `--warning`, `--danger`, `--info`, each with a `-soft` tint (`--success-soft`, `--muted-soft`, ...) |
| Charts | `--series-1` … `--series-8`, `--kind-images`, `--kind-volumes`, `--kind-layers`, `--kind-cache` |
| Type | `--sans`, `--mono` |
| Space | `--space-1` (4px) … `--space-7` (48px), `--pad`, `--row` |
| Radius | `--radius-sm` 4, `--radius-xs` 6, `--radius` 7, `--radius-md` 9, `--radius-lg` 12, `--radius-pill` |
| Elevation | `--shadow`, `--shadow-pop`, `--hi` |
| Shell | `--sidebar-w`, `--topbar-h` |

Never hard-code colours. Every colour comes from a token, so dark mode works.

## Vocabulary

- Status words go in `StatusBadge status="running|starting|stopped|failed|…"`. It maps Docker states to tones itself. Use `Led` for a bare status light and `ServiceCells` for per-service health in a row.
- Section and table headings use `PanelLabel` (mono, uppercase), not `<h3>` styling.
- Image refs, digests, ports and env pairs are mono: `Tag` (mono by default) or `font-family: var(--mono)`.
- `Button` needs a `variant` (`primary|secondary|ghost|danger`). Prefer `disabledReason="…"` over bare `disabled` so the reason is discoverable. Destructive confirmations go through `ConfirmDialog danger`.
- `Field` takes a render function. Spread its control props onto your input: `{({ readOnly: _, ...c }) => <input {...c} />}`. Bare `<input>`/`<select>` inside `Field` are styled by it.
- `Dialog`/`ConfirmDialog` portal to `document.body` and render only when `open`.

## Where the truth lives

Read `components/<group>/<Name>/<Name>.prompt.md` and `<Name>.d.ts` for each component's API. Read `_ds_bundle.css` for the token values and the component styles.

## Example

```jsx
const { PanelLabel, StatusBadge, ServiceCells, Tag, Button, RelativeTime } = window.Stackstead;

<section style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: 'var(--space-4)', display: 'grid', gap: 'var(--space-3)' }}>
  <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--space-2)' }}>
    <PanelLabel as="h2">monitoring</PanelLabel>
    <StatusBadge status="running" />
    <span style={{ marginLeft: 'auto', color: 'var(--muted)' }}>deployed <RelativeTime iso={deployedAt} /></span>
  </div>
  <div style={{ display: 'flex', gap: 'var(--space-2)', alignItems: 'center' }}>
    <ServiceCells count={4} good={3} bad={0} starting={1} title="3 running, 1 starting" />
    <Tag>grafana/grafana:11.2</Tag>
  </div>
  <div style={{ display: 'flex', gap: 'var(--space-2)', justifyContent: 'flex-end' }}>
    <Button variant="secondary" size="sm">View logs</Button>
    <Button variant="primary" size="sm">Redeploy</Button>
  </div>
</section>
```
