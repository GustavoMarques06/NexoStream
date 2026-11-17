import { ArrowLeft, ArrowRight, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useBranding } from "../../branding";
import { ARTICLES, GROUPS } from "./articles";

const norm = (s: string) =>
  s
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "");

/** Docs: the user guide of the panel, with the configured brand name. */
export function Docs() {
  const { slug } = useParams();
  const brand = useBranding();
  const [q, setQ] = useState("");
  const top = useRef<HTMLDivElement>(null);
  const index = Math.max(
    0,
    ARTICLES.findIndex((a) => a.slug === slug),
  );
  const article = ARTICLES[index]!;
  const prev = ARTICLES[index - 1];
  const next = ARTICLES[index + 1];

  useEffect(() => {
    top.current?.scrollIntoView({ block: "start" });
  }, [slug]);

  const shown = useMemo(() => {
    const words = norm(q).split(/\s+/).filter(Boolean);
    if (!words.length) return ARTICLES;
    return ARTICLES.filter((a) => {
      const text = norm(`${a.title} ${a.keywords} ${a.group}`);
      return words.every((w) => text.includes(w));
    });
  }, [q]);

  return (
    <div className="docs" ref={top}>
      <aside className="docs-nav">
        <div className="docs-brand">Documentação · {brand.name}</div>
        <label className="search docs-search">
          <Search size={14} />
          <input value={q} placeholder="Buscar na documentação" onChange={(e) => setQ(e.target.value)} />
        </label>
        {GROUPS.map((g) => {
          const items = shown.filter((a) => a.group === g);
          if (!items.length) return null;
          return (
            <div key={g} className="docs-group">
              <div className="nav-label">{g}</div>
              {items.map((a) => (
                <Link
                  key={a.slug}
                  to={`/docs/${a.slug}`}
                  className={`docs-link ${a.slug === article.slug ? "on" : ""}`}
                  onClick={() => setQ("")}
                >
                  <a.icon size={15} /> {a.title}
                </Link>
              ))}
            </div>
          );
        })}
        {!shown.length && (
          <div className="small muted" style={{ padding: "8px 10px" }}>
            Nada encontrado.
          </div>
        )}
      </aside>

      <article className="docs-article">
        <div className="docs-crumb">{article.group}</div>
        <h1>
          <article.icon size={22} /> {article.title}
        </h1>
        <div className="docs-body">{article.body(brand.name)}</div>
        <nav className="docs-pager">
          {prev ? (
            <Link to={`/docs/${prev.slug}`} className="docs-pager-link">
              <ArrowLeft size={15} />
              <span>
                <small>Anterior</small>
                {prev.title}
              </span>
            </Link>
          ) : (
            <span />
          )}
          {next && (
            <Link to={`/docs/${next.slug}`} className="docs-pager-link next">
              <span>
                <small>Próximo</small>
                {next.title}
              </span>
              <ArrowRight size={15} />
            </Link>
          )}
        </nav>
      </article>
    </div>
  );
}
