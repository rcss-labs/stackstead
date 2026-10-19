import { mediaQuery } from '$lib/stores/media';
import { useStore } from './useStore';

// Keep in step with the single literal breakpoint in the shell CSS (README: mobile is < 760px).
const isMobileQuery = mediaQuery('(max-width: 759px)');

export function useIsMobile(): boolean {
  return useStore(isMobileQuery);
}
