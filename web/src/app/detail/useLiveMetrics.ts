import { useEffect, useState } from 'react';
import { createStackLiveMetrics, type StackLiveMetrics } from '$lib/stacks/liveMetrics';

/**
 * One live metrics stream per mounted screen, shared by the rail and the Metrics tab. The stream is open
 * only while `needed`; releasing it in the effect cleanup also covers unmount and a→b navigation (the
 * route is keyed on the name). close() is terminal, so it is not called from an effect that StrictMode
 * replays: the last release already ends the request.
 */
export function useLiveMetrics(name: string, needed: boolean): StackLiveMetrics {
  const [live] = useState(() => createStackLiveMetrics(name));
  useEffect(() => {
    if (!needed) return;
    return live.acquire();
  }, [live, needed]);
  return live;
}
