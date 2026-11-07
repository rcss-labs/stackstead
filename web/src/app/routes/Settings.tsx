import { Navigate, useLocation, useParams } from 'react-router';
import { SettingsScreen } from '../settings/SettingsScreen';

// App owns the main landmark. SettingsScreen reads the section itself with useParams().
export default function Settings() {
  const { section } = useParams();
  const { search, hash } = useLocation();
  // Old bookmarks to /settings land on the first section; a fragment from the single-page layout is kept for the section to read.
  if (section === undefined) return <Navigate replace to={{ pathname: '/settings/system', search, hash }} />;
  return <SettingsScreen />;
}
