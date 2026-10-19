import { useEffect, useState } from 'react';

const currentSeconds = () => Math.floor(Date.now() / 1000);

/** Unix seconds that advance every `intervalMs` while the tab is visible and refresh when it becomes visible again. */
export function useNowSeconds(intervalMs: number): number {
  const [now, setNow] = useState(currentSeconds);
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    const stop = () => {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
    };
    const start = () => {
      stop();
      timer = setInterval(() => setNow(currentSeconds()), intervalMs);
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') stop();
      else {
        setNow(currentSeconds());
        start();
      }
    };
    if (document.visibilityState !== 'hidden') start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [intervalMs]);
  return now;
}
