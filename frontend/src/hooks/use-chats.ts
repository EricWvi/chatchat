import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";

/**
 * The chats the user created or joined. Other people's activity changes
 * the online counts and order, so the list refreshes every 15 seconds; the
 * open chat also refreshes it when its own participants change.
 */
export function useChats() {
  return useQuery({ queryKey: ["chats"], queryFn: api.chats, refetchInterval: 15_000 });
}
