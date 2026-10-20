import { useParams } from 'react-router';
import { ContainerDetailScreen } from '../containers/ContainerDetailScreen';

/** Keyed on the name so a jump from one container to another resets every piece of page state. */
export default function ContainerDetail() {
  const { name = '' } = useParams();
  return <ContainerDetailScreen key={name} name={name} />;
}
