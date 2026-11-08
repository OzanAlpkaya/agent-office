import { useEffect, useState } from "react";

// useMedia follows a CSS media query, e.g. the phone layout (ADR 0040).
export function useMedia(query: string): boolean {
  const [match, setMatch] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const on = (): void => setMatch(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [query]);
  return match;
}

// narrowQuery is the width below which the UI uses its phone layout.
export const narrowQuery = "(max-width: 760px)";
