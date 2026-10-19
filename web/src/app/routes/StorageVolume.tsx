import { useParams } from 'react-router';

/** Placeholder until Plan 21; App owns the main landmark. */
export default function StorageVolume() {
  const { name = '' } = useParams();
  return <h1>Volume {name}</h1>;
}
