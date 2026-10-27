import { useQuery } from "@tanstack/react-query";

import { api, type Limits } from "@/lib/api";

// Used until /meta answers; they match the server defaults.
const fallback: Limits = {
  presence_lease_seconds: 90,
  idle_destroy_after_seconds: 1800,
  max_chat_lifetime_seconds: 86400,
  wait_max_seconds: 60,
  page_size_default: 50,
  page_size_max: 200,
  message_max_chars: 16000,
  display_name_max_chars: 48,
  summary_threshold: 20,
};

/** The server's limits. */
export function useLimits(): Limits {
  const meta = useQuery({ queryKey: ["meta"], queryFn: api.meta, staleTime: Infinity });
  return meta.data?.limits ?? fallback;
}
