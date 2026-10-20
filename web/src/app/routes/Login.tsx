import { LoginScreen } from '../auth/LoginScreen';

/** App mounts this in `main.bare` when the session is anonymous. */
export default function Login() {
  return <LoginScreen />;
}
