import { useEffect, useState } from 'react';
import { relativeTime } from '$lib/format';

export function RelativeTime({ iso }: { iso: string }) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 30_000);
    return () => clearInterval(timer);
  }, []);

  const parsed = new Date(iso);
  const absolute = Number.isNaN(parsed.getTime()) ? undefined : parsed.toLocaleString();
  return (
    <time dateTime={iso} title={absolute}>
      {relativeTime(iso, now)}
    </time>
  );
}
