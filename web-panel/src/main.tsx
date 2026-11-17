import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { api, isLoggedIn, onSessionChange } from "./api";
import { Layout } from "./components/Layout";
import { MeContext } from "./me";
import { ChannelDetail } from "./pages/ChannelDetail";
import { Channels } from "./pages/Channels";
import { Dashboard } from "./pages/Dashboard";
import { Alerts } from "./pages/Alerts";
import { Events } from "./pages/Events";
import { Login } from "./pages/Login";
import { Mosaic } from "./pages/Mosaic";
import { Docs } from "./pages/docs/Docs";
import { Sessions } from "./pages/Sessions";
import { Settings } from "./pages/Settings";
import { Stats } from "./pages/Stats";
import type { Principal } from "./types";
import { loadBranding } from "./branding";
import "./styles.css";

void loadBranding();

function App() {
  const [logged, setLogged] = useState(isLoggedIn());
  const [me, setMe] = useState<Principal | null>(null);

  useEffect(() => onSessionChange(() => setLogged(isLoggedIn())), []);
  useEffect(() => {
    if (!logged) {
      setMe(null);
      return;
    }
    api
      .get<Principal>("/auth/me")
      .then(setMe)
      .catch(() => setMe(null));
  }, [logged]);

  if (!logged) return <Login />;
  return (
    <MeContext.Provider value={me}>
      <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<Dashboard />} />
            <Route path="channels" element={<Channels />} />
            <Route path="channels/:id" element={<ChannelDetail />} />
            <Route path="mosaic" element={<Mosaic />} />
            <Route path="dvr" element={<Navigate to="/settings?tab=dvr" replace />} />
            <Route path="settings" element={<Settings />} />
            <Route path="config" element={<Navigate to="/settings?tab=editor" replace />} />
            <Route path="workers" element={<Navigate to="/settings?tab=servers" replace />} />
            <Route path="profiles" element={<Navigate to="/settings?tab=profiles" replace />} />
            <Route path="events" element={<Events />} />
            <Route path="alerts" element={<Alerts />} />
            <Route path="stats" element={<Stats />} />
            <Route path="sessions" element={<Sessions />} />
            <Route path="docs" element={<Docs />} />
            <Route path="docs/:slug" element={<Docs />} />
            <Route path="audit" element={<Navigate to="/settings?tab=audit" replace />} />
            <Route path="users" element={<Navigate to="/settings?tab=users" replace />} />
            <Route path="tokens" element={<Navigate to="/settings?tab=tokens" replace />} />
            <Route path="*" element={<p>Página não encontrada.</p>} />
          </Route>
        </Routes>
      </BrowserRouter>
    </MeContext.Provider>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
