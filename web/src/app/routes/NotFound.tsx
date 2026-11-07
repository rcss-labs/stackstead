import { Link } from 'react-router';
import { palette } from '$lib/stores/palette';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { useServer } from '../servers/ServerContext';

/** The fallback view: the EmptyState heading names the page. */
export default function NotFound() {
  const { href } = useServer();
  return (
    <EmptyState as="h1" title="Page not found" description="That page does not exist. The link may be out of date, or it may point to another server.">
      <Link to={href('/')}>Go to Overview</Link>
      <Button variant="secondary" onClick={() => palette.set(true)}>Search</Button>
    </EmptyState>
  );
}
