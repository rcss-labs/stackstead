import { Link } from 'react-router';
import { EmptyState } from '../components/EmptyState';

/** Same view as App.svelte's fallback: the EmptyState heading names the page. */
export default function NotFound() {
  return (
    <EmptyState title="Page not found" description="That page does not exist.">
      <Link to="/">Back to stacks</Link>
    </EmptyState>
  );
}
