import { Link } from 'react-router';
import { EmptyState } from '../components/EmptyState';

import { useServer } from '../servers/ServerContext';
/** The fallback view: the EmptyState heading names the page. */
export default function NotFound() {
  const { href } = useServer();
  return (
    <EmptyState title="Page not found" description="That page does not exist.">
      <Link to={href('/')}>Back to stacks</Link>
    </EmptyState>
  );
}
