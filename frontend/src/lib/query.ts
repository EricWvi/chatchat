import { QueryClient } from "@tanstack/react-query";

import { ApiError } from "@/lib/api";
import { goToLogin } from "@/lib/session";

/** A lost login sends the browser to sign in again, wherever it surfaced. */
function onError(err: unknown) {
  if (err instanceof ApiError && err.code === "unauthenticated") goToLogin();
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
      refetchOnWindowFocus: false,
    },
    mutations: { onError },
  },
});
queryClient.getQueryCache().subscribe((e) => {
  if (e.type === "updated" && e.action.type === "error") onError(e.action.error);
});
