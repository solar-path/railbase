import { render } from "preact";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./app";
import "./styles.css";

// One QueryClient per process. We disable refetchOnWindowFocus
// because the admin UI is a long-lived single-tab tool — the noise
// from refetching on every Cmd-Tab is bigger than the staleness
// risk. Mutations explicitly invalidate what they touch.
//
// Stack note (Preact migration): React's `createRoot` has no
// preact/compat counterpart — Preact uses the top-level `render(vnode,
// container)`. The Vite alias map (react → preact/compat) handles every
// OTHER file in the app; this entry point is the one exception. We
// also drop <StrictMode> — Preact has no equivalent and the noisy
// double-render it triggers in React 18+ was the only reason it was
// here.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
      staleTime: 30_000,
    },
  },
});

const root = document.getElementById("root");
if (!root) throw new Error("admin: missing #root");

render(
  <QueryClientProvider client={queryClient}>
    <App />
  </QueryClientProvider>,
  root,
);
