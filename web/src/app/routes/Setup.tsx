import { SetupScreen } from '../auth/SetupScreen';

/** App mounts this in `main.bare` when the server needs first-run setup. */
export default function Setup() {
  return <SetupScreen />;
}
