import { useEffect, useState } from "react";

/** A clock that ticks, for "4m ago" and durations. Use it in the components
 * that show times, so the rest of the page doesn't re-render every second. */
export function useNow(every = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}
