import { useParams } from 'react-router';

/** Placeholder until Plan 21; App owns the main landmark. */
export default function StorageImage() {
  const { id = '' } = useParams();
  return <h1>Image {id}</h1>;
}
