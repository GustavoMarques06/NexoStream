import { useSyncExternalStore } from "react";

/** Panel branding (whitelabel), edited in Settings › Geral. */
export interface Branding {
  name: string;
  tagline: string;
  color: string; // #rrggbb or "" (default palette)
  logo: string; // data URL or "" (default logo)
  login_message: string;
}

export const DEFAULT_BRANDING: Branding = {
  name: "NexoStream",
  tagline: "Media Server",
  color: "",
  logo: "",
  login_message: "",
};

let current: Branding = DEFAULT_BRANDING;
const listeners = new Set<() => void>();

const ACCENT_VARS = ["--accent", "--accent-btn", "--accent-2", "--accent-soft", "--brand"];

/** The accent color the panel uses until a brand color is set. */
export const DEFAULT_ACCENT = "#01aaaf";

/** Path of the generic mark inside a 48x48 square (a play symbol). */
const MARK_PATH = "M19 14.5v19a1.2 1.2 0 0 0 1.8 1l15.5-9.5a1.2 1.2 0 0 0 0-2L20.8 13.5a1.2 1.2 0 0 0-1.8 1z";

/** genericIcon is the default tab icon: the generic mark in the accent color. */
function genericIcon(color: string): string {
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="11" fill="${color}"/>` +
    `<path d="${MARK_PATH}" fill="#fff"/></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** apply puts the branding on the page: title, favicon and accent color. */
export function applyBranding(b: Branding) {
  document.title = b.tagline ? `${b.name} · ${b.tagline}` : b.name;
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (icon) {
    // Without a custom logo the tab shows the generic mark in the brand color.
    icon.href = b.logo || genericIcon(b.color || DEFAULT_ACCENT);
    icon.type = b.logo ? b.logo.slice(5, b.logo.indexOf(";")) : "image/svg+xml";
  }
  const root = document.documentElement.style;
  if (b.color) {
    // Text and icons mix the color with the theme text color, so a very dark
    // or very light brand color stays readable in both themes.
    root.setProperty("--accent", `color-mix(in srgb, ${b.color} 78%, var(--text))`);
    root.setProperty("--accent-btn", b.color);
    root.setProperty("--accent-2", `color-mix(in srgb, ${b.color} 70%, black)`);
    root.setProperty("--accent-soft", `color-mix(in srgb, ${b.color} 16%, transparent)`);
    root.setProperty("--brand", b.color);
  } else {
    for (const v of ACCENT_VARS) root.removeProperty(v);
  }
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", b.color || DEFAULT_ACCENT);
}

function set(b: Branding) {
  current = b;
  applyBranding(b);
  listeners.forEach((l) => l());
}

/** loadBranding reads the public branding (also before login). */
export async function loadBranding(): Promise<void> {
  try {
    const res = await fetch("/api/v1/branding");
    if (!res.ok) return;
    const b = (await res.json()) as Partial<Branding>;
    set({ ...DEFAULT_BRANDING, ...b, name: b.name || DEFAULT_BRANDING.name });
  } catch {
    // keep the default branding
  }
}

/** previewBranding shows unsaved changes; loadBranding restores the saved ones. */
export function previewBranding(b: Branding) {
  set(b);
}

export function useBranding(): Branding {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}

/**
 * DefaultMark is the generic logo shown until the installation uploads its own:
 * a neutral play symbol on a tile that follows the brand color (--brand), so it
 * is never someone else's logo.
 */
function DefaultMark({ name }: { name: string }) {
  return (
    <svg
      className="brand-logo"
      viewBox="0 0 48 48"
      width="40"
      height="40"
      role="img"
      aria-label={name}
      xmlns="http://www.w3.org/2000/svg"
    >
      <rect width="48" height="48" rx="11" fill="var(--brand)" />
      <path d={MARK_PATH} fill="#fff" />
    </svg>
  );
}

/** BrandMark is the logo and product name of the sidebar and the login page. */
export function BrandMark() {
  const b = useBranding();
  return (
    <div className="brand">
      {b.logo ? (
        <span className="brand-mark custom">
          <img className="brand-logo" src={b.logo} alt={b.name} />
        </span>
      ) : (
        <span className="brand-mark">
          <DefaultMark name={b.name} />
        </span>
      )}
      <span className="brand-product">
        <strong>{b.name}</strong>
        {b.tagline && ` · ${b.tagline}`}
      </span>
    </div>
  );
}
