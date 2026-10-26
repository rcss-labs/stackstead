import { useParams } from 'react-router';
import { NetworksScreen } from '../networks/NetworksScreen';

// App owns the main landmark. Not keyed on the name: the list's filter and search survive a change of selection.
export default function NetworkList() {
  const { name } = useParams();
  return <NetworksScreen selected={name} />;
}
