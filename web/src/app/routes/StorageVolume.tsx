import { useParams } from 'react-router';
import { StorageVolumeScreen } from '../storage/StorageVolumeScreen';

/** Keyed on the name so a jump from one volume to another resets every piece of page state. */
export default function StorageVolume() {
  const { name = '' } = useParams();
  return <StorageVolumeScreen key={name} name={name} />;
}
