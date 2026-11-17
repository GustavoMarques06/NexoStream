import { ImageUp, RotateCcw, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import { DEFAULT_BRANDING } from "../branding";
import { Field, Section, type FieldErrors } from "../components/form";
import type { Settings } from "../types";

const MAX_LOGO_BYTES = 256 * 1024;
const LOGO_TYPES = ["image/png", "image/svg+xml", "image/jpeg", "image/webp"];

/** Whitelabel: product name, tagline, logo, accent color and login message. */
export function BrandingSection({
  s,
  up,
  errs,
}: {
  s: Settings;
  up: <K extends keyof Settings>(k: K, v: Settings[K]) => void;
  errs: FieldErrors;
}) {
  const file = useRef<HTMLInputElement>(null);
  const [logoError, setLogoError] = useState("");
  const color = s.brand_color || "#01aaaf";

  const pick = (f: File | undefined) => {
    setLogoError("");
    if (!f) return;
    if (!LOGO_TYPES.includes(f.type)) {
      setLogoError("Use PNG, SVG, JPEG ou WebP.");
      return;
    }
    if (f.size > MAX_LOGO_BYTES) {
      setLogoError("Imagem grande demais: até 256 KB.");
      return;
    }
    const r = new FileReader();
    r.onload = () => up("brand_logo", String(r.result));
    r.readAsDataURL(f);
  };

  return (
    <Section title="Marca (whitelabel)">
      <Field
        label="Nome do produto"
        hint="Aparece no painel, na tela de login, na aba do navegador e no arquivo de configuração."
        err={errs.system_name}
      >
        <input value={s.system_name} maxLength={64} onChange={(e) => up("system_name", e.target.value)} />
      </Field>
      <Field
        label="Subtítulo"
        hint="Ao lado do nome, ex.: Media Server, IPTV. Vazio = só o nome."
        err={errs.brand_tagline}
      >
        <input
          value={s.brand_tagline}
          maxLength={64}
          placeholder={DEFAULT_BRANDING.tagline}
          onChange={(e) => up("brand_tagline", e.target.value)}
        />
      </Field>
      <Field
        label="Cor principal"
        hint="Botões, links e destaques. Ajustada sozinha para os temas claro e escuro."
        err={errs.brand_color}
      >
        <div className="color-pick">
          <input type="color" value={color} onChange={(e) => up("brand_color", e.target.value)} />
          <input
            className="mono"
            value={s.brand_color}
            placeholder="padrão"
            maxLength={7}
            onChange={(e) => up("brand_color", e.target.value)}
          />
          {s.brand_color && (
            <button
              type="button"
              className="ghost sm"
              title="Cor padrão"
              onClick={() => up("brand_color", "")}
            >
              <RotateCcw size={14} />
            </button>
          )}
        </div>
      </Field>
      <Field
        label="Logo"
        hint="PNG, SVG, JPEG ou WebP até 256 KB; fundo transparente fica melhor. Também vira o ícone da aba."
        err={logoError || errs.brand_logo}
      >
        <div className="logo-pick">
          <div className="logo-preview">
            {s.brand_logo ? (
              <img src={s.brand_logo} alt="Logo" />
            ) : (
              <span className="small muted">padrão</span>
            )}
          </div>
          <input
            ref={file}
            type="file"
            accept={LOGO_TYPES.join(",")}
            hidden
            onChange={(e) => {
              pick(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
          <button type="button" className="secondary sm" onClick={() => file.current?.click()}>
            <ImageUp size={14} /> Enviar
          </button>
          {s.brand_logo && (
            <button
              type="button"
              className="ghost sm"
              title="Voltar ao logo padrão"
              onClick={() => up("brand_logo", "")}
            >
              <Trash2 size={14} />
            </button>
          )}
        </div>
      </Field>
      <Field
        label="Mensagem na tela de login"
        hint="Ex.: telefone do suporte. Aparece para quem ainda não entrou."
        err={errs.brand_login_message}
      >
        <textarea
          rows={2}
          maxLength={300}
          value={s.brand_login_message}
          onChange={(e) => up("brand_login_message", e.target.value)}
        />
      </Field>
      <Field label="Prévia">
        <div className="brand-preview" style={{ ["--pv" as string]: color }}>
          <div className="brand-preview-bar">
            {s.brand_logo ? <img src={s.brand_logo} alt="" /> : <span className="brand-preview-dot" />}
            <strong>{s.system_name || DEFAULT_BRANDING.name}</strong>
            {s.brand_tagline && <span>· {s.brand_tagline}</span>}
          </div>
          <button type="button" className="brand-preview-btn" tabIndex={-1}>
            Botão
          </button>
        </div>
      </Field>
    </Section>
  );
}
