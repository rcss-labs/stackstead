import { useParams } from 'react-router';

/** Placeholder until Plan 19; App owns the main landmark. */
export default function ContainerDetail() {
  const { name = '' } = useParams();
  return <h1>{name}</h1>;
}
