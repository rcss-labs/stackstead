import { mediaQuery } from '$lib/stores/media';
import { useStore } from './useStore';

// README: mobile is < 760px. Keep in step with the 759px media query in Toasts.module.css and any shell module with a mobile rule.
const isMobileQuery = mediaQuery('(max-width: 759px)');

export function useIsMobile(): boolean {
  return useStore(isMobileQuery);
}
