import { useParams } from 'react-router';
import { StorageImageScreen } from '../storage/StorageImageScreen';

/** Keyed on the id so a jump from one image to another resets every piece of page state. */
export default function StorageImage() {
  const { id = '' } = useParams();
  return <StorageImageScreen key={id} id={id} />;
}
