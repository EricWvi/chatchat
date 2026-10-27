import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * The signed-in user. A missing session sends the browser to the login
 * page (lib/query.ts), unless the user signed out in this browser; then
 * the signed-out screen shows.
 */
export function useMe() {
  const signedOut = useSession((s) => s.signedOut);
  return useQuery({ queryKey: ["me"], enabled: !signedOut, queryFn: api.me, staleTime: Infinity });
}
