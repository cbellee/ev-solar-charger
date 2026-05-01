import { NavLink, Route, Routes } from "react-router-dom";
import clsx from "clsx";
import { useStateStream } from "@/hooks/useStateStream";
import DashboardPage from "@/pages/DashboardPage";
import HistoryPage from "@/pages/HistoryPage";
import SessionsPage from "@/pages/SessionsPage";
import EventsPage from "@/pages/EventsPage";
import UsagePage from "@/pages/UsagePage";
import { SignOutButton } from "@/auth/AuthProvider";

export default function App() {
  const { status, snapshot, error } = useStateStream();

  return (
    <div className="min-h-screen bg-gray-900 text-gray-100">
      <header className="border-b border-gray-800 bg-gray-950/60 backdrop-blur sticky top-0 z-10">
        <div className="mx-auto max-w-7xl px-4 py-3 flex items-center justify-between flex-wrap gap-3">
          <div className="flex items-center gap-3">
            <span className="text-xl">⚡</span>
            <h1 className="text-lg font-semibold">Solar EV Charger</h1>
            {snapshot?.testMode && (
              <span className="rounded bg-amber-500/20 px-2 py-0.5 text-xs font-medium text-amber-300">
                TEST MODE
              </span>
            )}
          </div>
          <nav className="flex items-center gap-1">
            {[
              { to: "/", label: "Dashboard", end: true },
              { to: "/history", label: "History" },
              { to: "/sessions", label: "Sessions" },
              { to: "/events", label: "Events" },
              { to: "/usage", label: "Usage" },
            ].map(({ to, label, end }) => (
              <NavLink
                key={to}
                to={to}
                end={end}
                className={({ isActive }) =>
                  clsx(
                    "rounded px-3 py-1.5 text-sm font-medium transition-colors",
                    isActive
                      ? "bg-gray-800 text-white"
                      : "text-gray-400 hover:bg-gray-800/60 hover:text-gray-200",
                  )
                }
              >
                {label}
              </NavLink>
            ))}
          </nav>
          <div className="flex items-center gap-4 text-xs text-gray-400">
            <span className="flex items-center gap-2">
              <span
                className={clsx(
                  "h-2 w-2 rounded-full",
                  status === "open"
                    ? "bg-green-400"
                    : status === "error" || status === "auth-error"
                      ? "bg-red-500"
                      : "bg-amber-400 animate-pulse",
                )}
              />
              {status === "auth-error" ? "auth required" : status}
            </span>
            {error && status !== "open" && (
              <span className="max-w-xs truncate text-red-300" title={error}>
                {error}
              </span>
            )}
            <SignOutButton />
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-7xl px-4 py-6">
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/sessions" element={<SessionsPage />} />
          <Route path="/events" element={<EventsPage />} />
          <Route path="/usage" element={<UsagePage />} />
          <Route path="*" element={<DashboardPage />} />
        </Routes>
      </main>
    </div>
  );
}
