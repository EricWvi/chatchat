import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/App";
import { queryClient } from "@/lib/query";

import "./index.css";

// Pointer clicks on buttons and links leave no focus ring behind; the
// keyboard still focuses them.
document.addEventListener("mousedown", (e) => {
  const t = e.target as Element;
  if (t.closest("button, a, [role=menuitem]") && !t.closest("input, textarea")) e.preventDefault();
});

// Show the scrollbar while an element scrolls and for 800ms after.
{
  const timers = new WeakMap<Element, ReturnType<typeof setTimeout>>();
  document.addEventListener(
    "scroll",
    (e) => {
      const el = e.target === document ? document.documentElement : (e.target as Element);
      el.classList.add("scrolling");
      clearTimeout(timers.get(el));
      timers.set(
        el,
        setTimeout(() => el.classList.remove("scrolling"), 800),
      );
    },
    true,
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
