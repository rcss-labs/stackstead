import { useParams } from 'react-router';

/** Placeholder until Plan 18; App owns the main landmark. */
export default function StackDetail() {
  const { name = '' } = useParams();
  return <h1>{name}</h1>;
}
