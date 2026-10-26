import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./App";
import { AuthProvider } from "./auth/AuthProvider";
import { isAuthResponseInChildWindow } from "./auth/redirectBridge";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

function renderApp(): void {
  const root = document.getElementById("root");
  if (!root) throw new Error("missing #root");

  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <AuthProvider>
            <App />
          </AuthProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </StrictMode>,
  );
}

if (isAuthResponseInChildWindow(window)) {
  // Hidden-iframe or popup leg of an MSAL flow: hand the response to the
  // main window instead of booting a second copy of the app.
  void import("@azure/msal-browser/redirect-bridge").then(
    ({ broadcastResponseToMainFrame }) => broadcastResponseToMainFrame(),
  );
} else {
  renderApp();
}
