import { useParams } from 'react-router';
import { StackDetailScreen } from '../detail/StackDetailScreen';

/** Keyed on the name so /stacks/a → /stacks/b remounts the screen and restarts its controller, streams and title. */
export default function StackDetail() {
  const { name = '' } = useParams();
  return <StackDetailScreen key={name} name={name} />;
}
