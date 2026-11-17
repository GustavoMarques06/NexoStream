import { BrandMark } from "../branding";
import {
  Activity,
  BarChart3,
  BookOpen,
  Bell,
  LayoutDashboard,
  MonitorPlay,
  LogOut,
  Settings2,
  Tv,
  Users,
  type LucideIcon,
} from "lucide-react";
import { useEffect } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { logout } from "../api";
import { useApi } from "../hooks";
import { startLive, useLive } from "../live";
import { useMe } from "../me";
import type { AlertsResponse } from "../pages/Alerts";
import type { Summary } from "../types";

type Link = { to: string; label: string; icon: LucideIcon; scope?: string };

const MAIN: Link[] = [
  { to: "/", label: "Visão geral", icon: LayoutDashboard },
  { to: "/channels", label: "Canais", icon: Tv },
  { to: "/mosaic", label: "Mosaico", icon: MonitorPlay },
  { to: "/events", label: "Eventos", icon: Activity },
  { to: "/sessions", label: "Sessões", icon: Users },
  { to: "/stats", label: "Estatísticas", icon: BarChart3 },
  { to: "/alerts", label: "Alertas", icon: Bell },
];
const CONFIG: Link[] = [
  { to: "/settings", label: "Configurações", icon: Settings2 },
  { to: "/docs", label: "Documentação", icon: BookOpen },
];

export function Layout() {
  const me = useMe();
  const live = useLive();
  useEffect(startLive, []);
  const summary = useApi<Summary>("/system/summary", 15000);
  const problems = summary.data?.failing.length ?? 0;
  const alerts = useApi<AlertsResponse>("/alerts", 15000);
  const alertCount = (alerts.data?.counts.critical ?? 0) + (alerts.data?.counts.warning ?? 0);
  const critical = (alerts.data?.counts.critical ?? 0) > 0;
  const show = (l: Link) => !l.scope || (me?.scopes.includes(l.scope) ?? false);
  const item = (l: Link) => (
    <NavLink key={l.to} to={l.to} end={l.to === "/"}>
      <l.icon size={17} />
      {l.label}
      {l.to === "/channels" && problems > 0 && <span className="nav-count">{problems}</span>}
      {l.to === "/alerts" && alertCount > 0 && (
        <span className={`nav-count ${critical ? "" : "warn"}`}>{alertCount}</span>
      )}
    </NavLink>
  );
  return (
    <div className="shell">
      <aside className="sidebar">
        <BrandMark />
        <nav className="nav">
          {MAIN.filter(show).map(item)}
          <div className="nav-label">Configuração</div>
          {CONFIG.filter(show).map(item)}
        </nav>
        <div className="sidebar-foot">
          <span className="avatar">{me?.actor.slice(0, 2) ?? "?"}</span>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="truncate" style={{ fontWeight: 600, fontSize: 13 }}>
              {me?.actor}
            </div>
            <span className={`live-dot ${live.connected ? "on" : ""}`}>
              {live.connected ? "Ao vivo" : "Offline"}
            </span>
          </div>
          <button className="icon" onClick={logout} title="Sair" aria-label="Sair">
            <LogOut size={16} />
          </button>
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}
