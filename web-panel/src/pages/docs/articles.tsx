import {
  Activity,
  AlertTriangle,
  Archive,
  BarChart3,
  BookOpen,
  CalendarDays,
  Code2,
  Cpu,
  FileCode2,
  KeyRound,
  LayoutDashboard,
  LifeBuoy,
  Link2,
  Lock,
  MonitorPlay,
  Palette,
  Radio,
  Rocket,
  Server,
  Settings2,
  ShieldCheck,
  Tv,
  Users,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

/** Building blocks of the articles. */
export function Code({ children }: { children: string }) {
  return <pre className="doc-code">{children}</pre>;
}
export function Tip({ children }: { children: ReactNode }) {
  return <div className="doc-callout tip">{children}</div>;
}
export function Warn({ children }: { children: ReactNode }) {
  return <div className="doc-callout warn">{children}</div>;
}
export function Table({ head, rows }: { head: string[]; rows: ReactNode[][] }) {
  return (
    <div className="doc-table">
      <table>
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((c, j) => (
                <td key={j}>{c}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
const D = ({ to, children }: { to: string; children: ReactNode }) => (
  <Link to={`/docs/${to}`}>{children}</Link>
);

export interface Article {
  slug: string;
  group: string;
  title: string;
  icon: LucideIcon;
  /** Plain words for the search box. */
  keywords: string;
  body: (brand: string) => ReactNode;
}

export const GROUPS = ["Começando", "Canais", "Acompanhamento", "Configuração", "Integração", "Ajuda"];

export const ARTICLES: Article[] = [
  // ------------------------------------------------------------------ Começando
  {
    slug: "introducao",
    group: "Começando",
    title: "O que é o painel",
    icon: BookOpen,
    keywords: "introdução visão geral servidor de mídia iptv cluster",
    body: (b) => (
      <>
        <p>
          O {b} recebe canais de TV ao vivo (por exemplo, multicast UDP de um headend), confere a qualidade do
          sinal, entrega o canal aos players em HLS, DASH e MPEG-TS, grava para timeshift e catch-up, e troca
          de fonte sozinho quando o sinal cai.
        </p>
        <p>Ele tem duas partes:</p>
        <ul>
          <li>
            <b>Servidores de mídia:</b> recebem as fontes, processam o vídeo e atendem os espectadores.
            Continuam no ar mesmo se o painel ou a API caírem.
          </li>
          <li>
            <b>Painel e controle:</b> onde você cadastra os canais e acompanha a operação. O controle escolhe
            em qual servidor cada canal roda e o move para outro se um servidor cair.
          </li>
        </ul>
        <h3>Como o painel está organizado</h3>
        <Table
          head={["Menu", "Para que serve"]}
          rows={[
            ["Visão geral", "Resumo da operação: canais no ar, com problema, servidores e eventos recentes."],
            ["Canais", "Lista de canais e a página de cada um, com as abas de configuração."],
            ["Mosaico", "Vários canais tocando ao mesmo tempo, com medidores de áudio."],
            ["Eventos", "Tudo o que aconteceu: quedas, trocas de fonte, reinícios."],
            ["Sessões", "Quem está assistindo agora e o histórico de 30 dias."],
            ["Estatísticas", "Audiência e recursos ao longo do tempo."],
            ["Alertas", "Problemas ativos que precisam de atenção."],
            ["Configurações", "Parâmetros do sistema, servidores, usuários, API e editor de texto."],
          ]}
        />
        <Tip>
          Comece por <D to="primeiros-passos">Primeiros passos</D>.
        </Tip>
      </>
    ),
  },
  {
    slug: "primeiros-passos",
    group: "Começando",
    title: "Primeiros passos",
    icon: Rocket,
    keywords: "começar criar primeiro canal tutorial",
    body: () => (
      <>
        <ol className="doc-steps">
          <li>
            <b>Confira os servidores.</b> Em Configurações › Servidores, veja se pelo menos um servidor de
            mídia está online. Sem servidor, os canais ficam em "Iniciando" com o motivo "aguardando worker".
          </li>
          <li>
            <b>Crie um canal.</b> Em Canais › Novo canal, informe nome e a fonte principal (ex.:{" "}
            <code>udp://239.1.1.1:1234</code>). Deixe o modo em <i>Automático</i>. Detalhes em{" "}
            <D to="criar-canal">Criar um canal</D>.
          </li>
          <li>
            <b>Acompanhe o início.</b> Em poucos segundos o canal passa a <i>No ar</i>. A aba Overview mostra
            o player, a taxa de entrada e a análise do sinal.
          </li>
          <li>
            <b>Adicione um backup.</b> Na aba Input, inclua uma segunda fonte. Se a principal cair, o canal
            troca sozinho (<D to="fontes-failover">Fontes e failover</D>).
          </li>
          <li>
            <b>Copie os links.</b> Na aba Output estão os endereços HLS, DASH e MPEG-TS para os players (
            <D to="links">Links de reprodução</D>).
          </li>
        </ol>
      </>
    ),
  },
  {
    slug: "estados",
    group: "Começando",
    title: "Estados do canal",
    icon: Activity,
    keywords: "estado no ar degradado backup falha parado recuperando trocando",
    body: () => (
      <>
        <p>O estado aparece ao lado do nome do canal e muda sozinho conforme o sinal.</p>
        <Table
          head={["Estado", "O que significa", "O que fazer"]}
          rows={[
            ["Parado", "Canal desligado pelo operador.", "Use Iniciar."],
            ["Iniciando", "O canal foi enviado a um servidor e está subindo.", "Aguarde alguns segundos."],
            ["No ar", "Recebendo a fonte principal e entregando as saídas.", "Nada."],
            [
              "Degradado",
              "A fonte ativa tem um problema (sem pacotes, taxa baixa ou faixa faltando) em confirmação.",
              "Se persistir, o canal troca de fonte sozinho.",
            ],
            ["Trocando fonte", "Mudando para a próxima fonte da lista.", "Nada."],
            ["Em backup", "No ar por uma fonte de backup.", "Verifique a fonte principal."],
            ["Recuperando", "A principal voltou; aguardando ela ficar estável para voltar.", "Nada."],
            [
              "Falha",
              "Nenhuma fonte disponível ou reinícios demais.",
              <D key="f" to="problemas">
                Veja Solução de problemas
              </D>,
            ],
          ]}
        />
        <Tip>
          Em <i>Falha</i>, o canal continua escutando todas as fontes e volta sozinho quando alguma reaparece.
          Por reinícios demais (6 em 10 min), tenta de novo após 10 min, ou antes com Reiniciar.
        </Tip>
      </>
    ),
  },

  // ------------------------------------------------------------------ Canais
  {
    slug: "criar-canal",
    group: "Canais",
    title: "Criar um canal",
    icon: Tv,
    keywords: "novo canal cadastro id nome grupo fonte modo",
    body: () => (
      <>
        <p>
          Em Canais, use <b>Novo canal</b> e preencha:
        </p>
        <Table
          head={["Campo", "Descrição"]}
          rows={[
            [
              "Identificador",
              "Usado nos links (ex.: globo-sp). Letras minúsculas, números e hífen. Não muda depois.",
            ],
            ["Nome e grupo", "Como o canal aparece no painel. O grupo ajuda a filtrar."],
            ["Fontes", "Endereços de entrada, em ordem de prioridade. A primeira é a principal."],
            ["Modo", "Automático, Passthrough ou Transcodificar (veja Transcodificação)."],
            ["Saídas", "HLS (padrão), MPEG-TS por HTTP, UDP multicast, DASH e push."],
            ["Gravação", "Liga o DVR com a retenção desejada."],
          ]}
        />
        <h3>Fontes aceitas</h3>
        <Table
          head={["Protocolo", "Exemplo"]}
          rows={[
            ["UDP / multicast", <code key="1">udp://239.1.1.1:1234</code>],
            ["RTP", <code key="2">rtp://239.1.1.1:5004</code>],
            ["SRT", <code key="3">srt://10.0.0.5:9000</code>],
            ["RTMP", <code key="4">rtmp://servidor/app/canal</code>],
            ["RTSP", <code key="5">rtsp://camera/stream</code>],
            ["HTTP / HLS", <code key="6">https://origem/live/index.m3u8</code>],
          ]}
        />
        <p>
          Para multicast em servidores com várias placas de rede, informe a <b>interface</b> de ingestão da
          fonte (ex.: <code>eno2</code>).
        </p>
      </>
    ),
  },
  {
    slug: "fontes-failover",
    group: "Canais",
    title: "Fontes e failover (aba Input)",
    icon: Radio,
    keywords: "input fonte backup failover troca principal taxa baixa sem pacotes erros cc",
    body: () => (
      <>
        <p>
          A aba <b>Input</b> mostra cada fonte com o estado (no ar, em espera, falha), a taxa recebida e o
          último pacote. Use <b>Editar</b> para mudar endereços, a ordem (setas) ou incluir backups (até 8
          fontes). <b>Ativar</b> troca a fonte manualmente.
        </p>
        <Warn>
          Ativar manualmente uma fonte de backup suspende a volta automática à principal até a próxima troca
          manual ou reinício.
        </Warn>
        <h3>Quando o canal troca de fonte</h3>
        <Table
          head={["Regra", "Padrão", "Significado"]}
          rows={[
            ["Sem pacotes", "3 s", "Nenhum pacote chegou nesse tempo."],
            ["Confirmação", "5 s", "Espera antes de trocar, para ignorar falhas curtas."],
            ["Taxa baixa", "0,3 × média por 10 s", "A taxa caiu abaixo de 30% da média."],
            ["Faixa ausente", "5 s", "Vídeo ou áudio sumiu."],
            ["Volta à principal", "após 60 s estável", "Retorno automático quando a principal se recupera."],
          ]}
        />
        <p>
          O painel <b>Sinal recebido</b> mostra codec, resolução, quadros por segundo, todas as faixas de
          áudio e legendas, e os erros de continuidade (CC) por minuto. Erros CC indicam perda de pacotes na
          rede de entrada.
        </p>
      </>
    ),
  },
  {
    slug: "transcodificacao",
    group: "Canais",
    title: "Transcodificação e ABR (aba Transcoder)",
    icon: Cpu,
    keywords:
      "transcoder transcodificação abr qualidades perfil passthrough automático encoder gpu nvenc qsv",
    body: () => (
      <>
        <Table
          head={["Modo", "Quando usar"]}
          rows={[
            [
              "Automático",
              "Recomendado. Repassa H.264/AAC sem recodificar e converte só o necessário (MP2/AC-3 → AAC, MPEG-2/HEVC → H.264).",
            ],
            ["Passthrough", "Nunca recodifica. Menor uso de CPU."],
            ["Transcodificar", "Sempre recodifica com o perfil escolhido. Necessário para ABR."],
          ]}
        />
        <h3>Várias qualidades (ABR)</h3>
        <p>
          No modo Transcodificar, marque de 1 a 6 perfis em <b>Qualidades</b>. Com dois ou mais, o canal
          publica uma playlist mestre e o player escolhe a qualidade pela banda. O vídeo é decodificado uma
          vez e codificado uma vez por qualidade. MPEG-TS e miniatura usam a qualidade mais alta; o DVR
          também, a menos que você ligue a gravação de todas as qualidades na aba DVR.
        </p>
        <p>
          O quadro <b>Processamento atual</b> mostra o que está rodando: entrada, decisão, encoder de cada
          qualidade e as saídas.
        </p>
        <h3>Encoders de hardware</h3>
        <p>
          Perfis com encoder <i>auto</i> usam NVIDIA NVENC ou Intel Quick Sync quando o servidor tem. Se o
          encoder de hardware falhar, ele fica 30 min fora de uso naquele servidor e o canal volta sozinho em
          CPU (evento "Encoder de hardware falhou").
        </p>
        <p>Os perfis (resolução, taxa, GOP, encoder) ficam em Configurações › Transcodificação.</p>
      </>
    ),
  },
  {
    slug: "dvr",
    group: "Canais",
    title: "Gravação, timeshift e catch-up (aba DVR)",
    icon: Archive,
    keywords: "dvr gravação timeshift catch-up retenção raid disco armazenamento",
    body: () => (
      <>
        <p>
          Ligue a gravação na aba <b>DVR</b> e escolha a retenção em dias. O servidor que roda o canal grava
          em arquivos de uma hora e apaga o que passar da retenção.
        </p>
        <Table
          head={["Onde gravar", "Descrição"]}
          rows={[
            ["Pasta padrão do servidor", "Definida por servidor em Configurações › Servidores."],
            [
              "Armazenamento",
              "Vários discos em RAID-0 (espalha) ou RAID-1 (espelha), criados em Configurações › DVR.",
            ],
            ["Pasta específica", "Um caminho absoluto no servidor."],
          ]}
        />
        <p>
          A linha do tempo mostra o que foi gravado; clique para assistir a partir daquele ponto. Os links de
          timeshift e catch-up estão no painel ao lado (<D to="links">formatos</D>). O espaço estimado é
          calculado pela taxa atual do canal.
        </p>
        <Tip>
          A cada 5 min as horas já gravadas são conferidas. Problemas viram o alerta "Gravação com problema".
        </Tip>
      </>
    ),
  },
  {
    slug: "saidas",
    group: "Canais",
    title: "Saídas e push (aba Output)",
    icon: Link2,
    keywords: "output saída hls dash mpeg-ts udp multicast push rtmp srt youtube legenda teletexto",
    body: () => (
      <>
        <Table
          head={["Saída", "Para quem"]}
          rows={[
            ["HLS", "Navegadores, celulares, Smart TVs e STBs. Ligada por padrão."],
            ["MPEG-DASH", "Players DASH e Smart TVs. Não disponível com DRM."],
            ["MPEG-TS por HTTP", "Fluxo contínuo para STBs e VLC. Não disponível com DRM."],
            ["MPEG-TS por UDP", "Multicast para a rede interna."],
            ["Push", "Envia o canal para outro servidor (CDN, YouTube, outro media server)."],
          ]}
        />
        <h3>Push</h3>
        <p>
          Adicione até 10 destinos <code>rtmp://</code>, <code>rtmps://</code> ou <code>srt://</code>. Cada
          destino reconecta sozinho se cair (esperando de 1 s a 30 s) e mostra estado, dados enviados e o
          último erro. A chave de stream fica escondida no status. RTMP leva um vídeo e um áudio; SRT leva
          todas as faixas.
        </p>
        <h3>Legendas</h3>
        <p>
          Legendas DVB e teletexto da entrada seguem nas saídas MPEG-TS (HTTP, UDP e push SRT). HLS, DASH e
          RTMP não carregam esse formato.
        </p>
      </>
    ),
  },
  {
    slug: "drm",
    group: "Canais",
    title: "Proteção DRM (AES-128)",
    icon: Lock,
    keywords: "drm criptografia aes-128 chave token proteção",
    body: (b) => (
      <>
        <p>
          Na aba Output, ligue <b>Proteção (DRM)</b>. O HLS e o DVR passam a sair criptografados com AES-128,
          e a chave só é entregue para links com um <b>token de reprodução</b> válido (
          <code>…/index.m3u8?token=…</code>). A chave muda a cada período configurado (padrão 1 hora).
        </p>
        <p>
          O painel gera o token sozinho para as prévias. Para os assinantes, o middleware da operadora assina
          os próprios tokens com a chave de tokens do {b} (<D to="api">formato</D>).
        </p>
        <Warn>
          Com DRM, MPEG-TS por HTTP e DASH ficam desativados, porque não são criptografados. AES-128 funciona
          em qualquer player HLS, mas não é DRM de estúdio (Widevine, PlayReady, FairPlay).
        </Warn>
      </>
    ),
  },
  {
    slug: "epg",
    group: "Canais",
    title: "Guia de programação (aba EPG)",
    icon: CalendarDays,
    keywords: "epg guia programação xmltv catch-up programa",
    body: () => (
      <>
        <p>
          Informe a URL de um arquivo <b>XMLTV</b> (<code>.xml</code> ou <code>.xml.gz</code>) e o ID do canal
          dentro dele (vazio = o identificador do canal). O guia é importado a cada 6 horas; use{" "}
          <b>Importar</b> para atualizar agora.
        </p>
        <p>
          A programação mostra ontem, hoje e amanhã, com o programa no ar destacado. Com o DVR ligado,{" "}
          <b>Assistir</b> abre o programa que já passou (catch-up).
        </p>
        <p>
          O guia de todos os canais fica em <code>/api/v1/epg.xml</code>, para players e middleware.
        </p>
      </>
    ),
  },
  {
    slug: "autorizacao",
    group: "Canais",
    title: "Controle de acesso (aba Auth)",
    icon: ShieldCheck,
    keywords: "auth autorização backend middleware ip bloqueio limite sessões token on_play",
    body: () => (
      <>
        <p>
          Com o controle ligado, cada nova sessão de espectador passa por estas regras, nesta ordem, no
          servidor de mídia:
        </p>
        <ol>
          <li>
            <b>IPs bloqueados</b> e <b>IPs liberados</b> (IP ou rede, ex.: <code>10.0.0.0/8</code>). Valem em
            todo pedido, até com token.
          </li>
          <li>
            <b>Token do sistema</b> (o mesmo do DRM): libera sem consultar o backend.
          </li>
          <li>
            <b>Backend de autorização</b>: uma URL do seu middleware, chamada a cada nova sessão.
          </li>
          <li>
            <b>Máximo de sessões</b> do canal por servidor.
          </li>
        </ol>
        <h3>Como o backend é chamado</h3>
        <Code>{`GET <backend>?name=<canal>&ip=<ip>&token=<token>&proto=hls|mpegts|dvr&user_agent=<player>&session_id=<id>

Resposta 2xx libera; qualquer outra nega.
Cabeçalhos opcionais da resposta:
  X-AuthDuration: 300     segundos até consultar de novo
  X-UserId: assinante-42  identifica o assinante nas sessões
  X-Max-Sessions: 2       limite de sessões desse assinante (somado entre os servidores)
  X-Unique: true          derruba as outras sessões do assinante`}</Code>
        <p>
          Se o backend não responder, o padrão é negar; você pode escolher liberar para manter o serviço no
          ar.
        </p>
      </>
    ),
  },
  {
    slug: "sessoes",
    group: "Canais",
    title: "Sessões de reprodução",
    icon: Users,
    keywords: "sessões espectadores play sessions encerrar histórico audiência ip assinante",
    body: () => (
      <>
        <p>
          Cada espectador é uma <b>sessão</b>: identificada pelo token do link, ou por IP + player quando não
          há token. Uma sessão HLS fecha depois de 30 s sem pedidos; uma conexão MPEG-TS fecha ao desconectar.
        </p>
        <p>
          A aba <b>Play sessions</b> do canal e a página <b>Sessões</b> mostram quem está assistindo agora
          (IP, assinante, player, protocolo, duração, dados e taxa). <b>Encerrar</b> derruba a sessão na hora
          e a bloqueia por 2 minutos.
        </p>
        <p>
          O <b>histórico</b> guarda as sessões encerradas por 30 dias. Filtre por período, assinante ou IP.
        </p>
        <Tip>
          Vários aparelhos iguais atrás do mesmo IP (NAT) e sem token contam como uma sessão. Use tokens para
          contar cada assinante.
        </Tip>
      </>
    ),
  },

  // ------------------------------------------------------------------ Acompanhamento
  {
    slug: "acompanhamento",
    group: "Acompanhamento",
    title: "Visão geral, mosaico e eventos",
    icon: LayoutDashboard,
    keywords: "visão geral dashboard mosaico eventos linha do tempo miniatura",
    body: () => (
      <>
        <p>
          <b>Visão geral</b> resume a operação: canais por estado, servidores, canais com problema e eventos
          recentes. Tudo atualiza em tempo real.
        </p>
        <p>
          <b>Canais</b> mostra cada canal com miniatura (atualizada a cada 15 s), taxa de entrada e estado.
          Use os filtros (No ar, Atenção, Parados) e a busca.
        </p>
        <p>
          <b>Mosaico</b> toca vários canais lado a lado, com medidores de áudio; clique num canal para
          ouvi-lo.
        </p>
        <p>
          <b>Eventos</b> lista tudo o que aconteceu: mudanças de estado, trocas de fonte, reinícios,
          servidores fora, problemas de gravação. A página de cada canal tem sua própria linha do tempo.
        </p>
      </>
    ),
  },
  {
    slug: "alertas",
    group: "Acompanhamento",
    title: "Alertas",
    icon: AlertTriangle,
    keywords: "alertas crítico aviso regras sem sinal disco cpu",
    body: () => (
      <>
        <p>
          A página <b>Alertas</b> mostra os problemas ativos, críticos primeiro. O contador no menu indica
          quantos há.
        </p>
        <Table
          head={["Alerta", "Quando aparece", "Gravidade"]}
          rows={[
            ["Canal sem sinal", "Canal em falha ou sem pacotes há mais de 10 s", "Crítico"],
            ["Fonte alternativa", "No backup há mais de 10 min", "Aviso"],
            ["Reinícios excessivos", "Mais de 5 reinícios em 10 min", "Aviso"],
            ["Erros de continuidade", "Mais de 100 erros CC por minuto", "Aviso"],
            ["Servidor indisponível", "Sem heartbeat; canais realocados", "Crítico"],
            ["Disco do DVR", "Acima de 85% (crítico acima de 95%) ou com falha", "Aviso / Crítico"],
            ["Gravação com problema", "Horas gravadas inconsistentes nas últimas 24 h", "Aviso"],
            ["Recursos", "CPU ou GPU acima de 90%", "Aviso"],
          ]}
        />
        <p>As mesmas regras existem no Prometheus, para notificar por e-mail ou chat.</p>
      </>
    ),
  },
  {
    slug: "estatisticas",
    group: "Acompanhamento",
    title: "Estatísticas",
    icon: BarChart3,
    keywords: "estatísticas audiência gráficos cpu memória espectadores mais assistidos",
    body: () => (
      <>
        <p>
          Uma amostra por minuto, guardada por 30 dias. Escolha o período (1 hora a 30 dias) no topo da
          página.
        </p>
        <ul>
          <li>Espectadores simultâneos, canais no ar e taxa de entrada total.</li>
          <li>Canais mais assistidos, em espectador-hora, com o pico de espectadores.</li>
          <li>CPU e memória de cada servidor.</li>
        </ul>
        <p>
          Passe o mouse sobre um gráfico para ler os valores; <b>Ver tabela</b> mostra os números.
        </p>
      </>
    ),
  },

  // ------------------------------------------------------------------ Configuração
  {
    slug: "configuracoes",
    group: "Configuração",
    title: "Configurações do sistema",
    icon: Settings2,
    keywords: "configurações geral entrada hls cluster segurança perfil padrão failover segmento",
    body: () => (
      <>
        <p>Só administradores alteram. Toda alteração fica registrada na Auditoria.</p>
        <Table
          head={["Aba", "O que contém"]}
          rows={[
            ["Geral", "Marca (whitelabel), perfil padrão e passthrough de HEVC."],
            ["Entrada", "Buffer UDP e o failover padrão dos canais novos."],
            ["Saídas HLS", "Duração do segmento (padrão 6 s) e tamanho da playlist (10)."],
            ["Transcodificação", "Perfis de saída (resolução, taxa, GOP, encoder)."],
            ["DVR", "Retenção padrão, armazenamentos (RAID) e canais gravando."],
            ["Servidores", "Estado, carga, drain e ajustes de cada servidor."],
            ["Cluster", "Limite de CPU e custos usados para distribuir os canais."],
            ["Segurança", "Validade das sessões do painel e tentativas de login."],
            ["Usuários, API e tokens, Auditoria", "Contas, integrações e registro de ações."],
            ["Editor de texto", "Toda a configuração em um arquivo de texto."],
          ]}
        />
        <p>
          Mudanças de HLS reiniciam o empacotamento dos canais por alguns segundos. Os servidores aplicam as
          mudanças sozinhos, sem reinício.
        </p>
      </>
    ),
  },
  {
    slug: "servidores",
    group: "Configuração",
    title: "Servidores de mídia",
    icon: Server,
    keywords: "servidores worker drain manutenção papéis interfaces dvr pasta url pública",
    body: () => (
      <>
        <p>
          Em Configurações › Servidores, cada servidor mostra estado, CPU, memória, rede, encoders e canais.
        </p>
        <ul>
          <li>
            <b>Drain:</b> para manutenção. O servidor deixa de receber canais e os atuais migram aos poucos
            para os outros.
          </li>
          <li>
            <b>Papéis:</b> Ingestão/repasse, Transcodificação e DVR. Um servidor com papéis só recebe canais
            compatíveis; sem papéis, recebe qualquer canal.
          </li>
          <li>
            <b>Interfaces de ingestão, pasta do DVR e URLs públicas</b> (base dos links mostrados no painel).
          </li>
        </ul>
        <p>Se um servidor ficar 15 s sem responder, seus canais vão para outro servidor automaticamente.</p>
      </>
    ),
  },
  {
    slug: "marca",
    group: "Configuração",
    title: "Marca (whitelabel)",
    icon: Palette,
    keywords: "marca whitelabel logo nome cor login",
    body: () => (
      <>
        <p>
          Em Configurações › Geral › <b>Marca</b>, defina nome do produto, subtítulo, cor principal, logo
          (PNG, SVG, JPEG ou WebP até 256 KB) e uma mensagem para a tela de login. A prévia mostra o resultado
          antes de salvar.
        </p>
        <p>
          A marca vale para o menu, a tela de login, o título e o ícone da aba do navegador, e para esta
          documentação.
        </p>
      </>
    ),
  },
  {
    slug: "usuarios",
    group: "Configuração",
    title: "Usuários, tokens e auditoria",
    icon: KeyRound,
    keywords: "usuários perfis administrador operador leitura tokens api auditoria senha",
    body: () => (
      <>
        <Table
          head={["Perfil", "Pode"]}
          rows={[
            ["Administrador", "Tudo, inclusive configurações, usuários e tokens."],
            [
              "Operador",
              "Criar e editar canais, iniciar, parar, reiniciar e trocar fontes, encerrar sessões.",
            ],
            ["Somente leitura", "Ver canais, eventos, sessões e estatísticas."],
          ]}
        />
        <p>
          <b>API e tokens:</b> crie tokens para integrações (o valor <code>nxs_…</code> aparece só na
          criação). Revogue quando não precisar mais.
        </p>
        <p>
          <b>Auditoria:</b> registra quem fez o quê e quando (criar canal, mudar configurações, encerrar
          sessão…).
        </p>
      </>
    ),
  },
  {
    slug: "editor",
    group: "Configuração",
    title: "Editor de texto",
    icon: FileCode2,
    keywords: "editor texto configuração arquivo sintaxe stream settings server",
    body: () => (
      <>
        <p>
          Toda a configuração em um arquivo, para revisar, copiar entre ambientes ou versionar. <b>Validar</b>{" "}
          mostra o que vai mudar antes de aplicar; <b>Aplicar</b> grava tudo de uma vez.
        </p>
        <Code>{`stream globo-sp {
  title "Globo SP";
  group Abertos;
  input udp://239.1.1.1:1234 interface=eno2;   # principal
  input udp://239.1.2.1:1234;                  # backup
  transcode hd sd;                             # ABR com duas qualidades
  output hls; output dash;
  push rtmp://cdn.exemplo/live/CHAVE;
  dvr 3d storage=principal;
  drm aes-128 rotate=1h;
  auth https://middleware/auth max_sessions=500 allow=10.0.0.0/8;
  epg https://epg.exemplo/guia.xml.gz id=globo.br;
}

settings {
  name "Minha TV";  brand_color "#0a7cff";
}

server media-01 {
  interface eno2;  roles passthrough dvr;
}`}</Code>
        <p>
          O botão <b>Ajuda</b> do editor lista todas as diretivas. O logo da marca é o único item que fica só
          no painel.
        </p>
      </>
    ),
  },

  // ------------------------------------------------------------------ Integração
  {
    slug: "links",
    group: "Integração",
    title: "Links de reprodução",
    icon: MonitorPlay,
    keywords: "links url m3u8 mpd stream.ts timeshift catch-up player",
    body: () => (
      <>
        <p>
          Os endereços exatos de cada canal estão na aba Output (botões de copiar). O formato, a partir do
          endereço público do servidor:
        </p>
        <Table
          head={["Saída", "Endereço"]}
          rows={[
            ["HLS ao vivo", <code key="1">/&lt;canal&gt;/index.m3u8</code>],
            ["MPEG-DASH", <code key="2">/&lt;canal&gt;/dash/index.mpd</code>],
            ["MPEG-TS por HTTP", <code key="3">/&lt;canal&gt;/stream.ts</code>],
            ["Timeshift (última hora)", <code key="4">/dvr/&lt;canal&gt;/index.m3u8</code>],
            [
              "Catch-up por intervalo",
              <code key="5">/dvr/&lt;canal&gt;/index-&lt;início&gt;-&lt;segundos&gt;.m3u8</code>,
            ],
            [
              "Timeshift desde um horário",
              <code key="6">/dvr/&lt;canal&gt;/index-&lt;início&gt;-now.m3u8</code>,
            ],
            ["Catch-up por datas", <code key="7">/dvr/&lt;canal&gt;/index.m3u8?from=…&amp;to=…</code>],
          ]}
        />
        <p>
          <code>&lt;início&gt;</code> é o horário em segundos Unix. Canais com DRM ou controle de acesso
          precisam de <code>?token=…</code> no link; o token é repetido automaticamente em todos os segmentos.
        </p>
      </>
    ),
  },
  {
    slug: "api",
    group: "Integração",
    title: "API e tokens de reprodução",
    icon: Code2,
    keywords: "api rest openapi token reprodução hmac middleware integração",
    body: (b) => (
      <>
        <p>
          A API REST fica em <code>/api/v1</code>, documentada em <code>/api/v1/openapi.json</code>.
          Autentique com um token de integração:
        </p>
        <Code>{`curl -H "Authorization: Bearer nxs_..." https://painel/api/v1/channels`}</Code>
        <h3>Token de reprodução</h3>
        <p>
          Para canais com DRM ou controle de acesso, o middleware pode gerar o token sozinho com a chave de
          tokens do {b} (arquivo <code>playback-token.key</code>):
        </p>
        <Code>{`expira = horário Unix em segundos
token  = expira + "." + hex(HMAC-SHA256(chave, "nexostream-play/v1\\n" + canal + "\\n" + expira))

link: https://origem/<canal>/index.m3u8?token=<token>`}</Code>
        <p>
          Ou pela API: <code>POST /api/v1/channels/&lt;canal&gt;/playback-token</code> com{" "}
          <code>{`{"ttl_seconds": 21600}`}</code>.
        </p>
      </>
    ),
  },

  // ------------------------------------------------------------------ Ajuda
  {
    slug: "problemas",
    group: "Ajuda",
    title: "Solução de problemas",
    icon: LifeBuoy,
    keywords: "problema erro falha sem sinal 403 503 player não toca push reconectando encoder",
    body: () => (
      <>
        <Table
          head={["Sintoma", "Causa provável", "O que fazer"]}
          rows={[
            [
              'Canal em Falha, "nenhuma fonte disponível"',
              "Nenhuma fonte entrega sinal.",
              "Confira a fonte (multicast, interface, firewall). O canal volta sozinho quando o sinal chega.",
            ],
            [
              'Canal em Falha, "limite de reinícios"',
              "O processamento caiu 6 vezes em 10 min.",
              "Veja o último erro no topo da página do canal e os eventos. Corrija e use Reiniciar.",
            ],
            [
              'Iniciando, "aguardando worker"',
              "Nenhum servidor online compatível.",
              "Veja Configurações › Servidores (estado, drain, papéis).",
            ],
            [
              "Erros CC altos",
              "Perda de pacotes na rede de entrada.",
              "Verifique a rede e o buffer UDP (Configurações › Entrada).",
            ],
            [
              "Player recebe 403",
              "Sem token, token vencido, IP bloqueado ou sessão encerrada.",
              "Gere um novo link com token e confira a aba Auth.",
            ],
            [
              "Player recebe 503 em canal com DRM",
              "O servidor não tem as chaves de DRM configuradas.",
              "Configure os arquivos de chave no servidor de mídia.",
            ],
            [
              'Push "reconectando"',
              "O destino recusa ou está fora.",
              "Confira URL e chave; o push reconecta sozinho.",
            ],
            [
              'Evento "Encoder de hardware falhou"',
              "Driver da GPU falhou.",
              "O canal já voltou em CPU. Atualize o driver; o encoder volta após 30 min.",
            ],
            [
              "Sem miniatura",
              "O canal ainda não gerou segmentos ou está parado.",
              "Aguarde alguns segundos com o canal no ar.",
            ],
          ]}
        />
      </>
    ),
  },
  {
    slug: "glossario",
    group: "Ajuda",
    title: "Glossário",
    icon: BookOpen,
    keywords: "glossário termos abr cc pmt gop passthrough",
    body: () => (
      <Table
        head={["Termo", "Significado"]}
        rows={[
          ["ABR", "Várias qualidades do mesmo canal; o player escolhe pela banda."],
          ["Catch-up", "Assistir um programa que já passou."],
          ["CC (continuity counter)", "Contador dos pacotes MPEG-TS; erros indicam pacotes perdidos."],
          ["Failover", "Troca automática para a fonte de backup."],
          ["GOP", "Intervalo entre quadros-chave; define onde os segmentos podem ser cortados."],
          ["Passthrough", "Repassar o vídeo sem recodificar."],
          ["PAT/PMT", "Tabelas do MPEG-TS que descrevem os programas e as faixas."],
          ["Push", "Enviar o canal para outro servidor."],
          ["Sessão", "Um espectador assistindo um canal."],
          ["Timeshift", "Voltar no tempo durante a transmissão ao vivo."],
          ["XMLTV", "Formato de arquivo do guia de programação."],
        ]}
      />
    ),
  },
];
