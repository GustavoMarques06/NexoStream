# 0009 — Sessões de reprodução, autorização e EPG por canal

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

No Flussonic, a página de cada stream tem as abas Overview, Input, Transcoder, DVR, Output, EPG, Auth e Play sessions. O operador vê e muda tudo de um canal num só lugar. Três dessas abas dependiam de coisas que o NexoStream não tinha:

- **Play sessions:** saber quem está assistindo. O HLS era servido direto pelo nginx, então o servidor de mídia não via os espectadores.
- **Auth:** decidir quem pode assistir, com um backend de autorização da operadora (o `on_play` do Flussonic), listas de IP e limite de sessões.
- **EPG:** a programação do canal, importada de um guia XMLTV.

## Decisão

- **O servidor de mídia serve todo o HLS.** O nginx de origem repassa `/<canal>/…` ao worker, junto com o IP do espectador em `X-Real-IP`. O worker só confia nesse cabeçalho quando a conexão vem do próprio nginx local (loopback).
- **Sessão de reprodução:** representa um espectador de um canal num servidor.
  - **HLS e DVR:** a sessão é identificada pelo token do link ou, sem token, por IP + User-Agent. Ela fecha depois de 30 s sem pedidos.
  - **MPEG-TS por HTTP:** cada conexão é uma sessão, que fecha quando a conexão cai.
  - **O que a sessão guarda:** IP, player, protocolo, assinante (`X-UserId`), bytes, pedidos, início e fim.
  - **Onde fica:** na memória do worker, com as últimas 50 encerradas por canal. O painel pede ao worker por comando (`sessions`).
  - **Encerrar pelo painel (`kill-session`):** a sessão é derrubada na hora, inclusive o fluxo MPEG-TS, e recusada por 2 min. A ação gera auditoria.
- **Autorização por canal** (`auth`), aplicada pelo worker:
  1. **IPs bloqueados e IPs liberados:** valem em todo pedido, inclusive para quem tem token.
  2. **Token de reprodução assinado pelo cluster** ([ADR 0008](0008-drm-aes-128-com-token.md)): libera sem consultar o backend.
  3. **Backend HTTP:** consultado com `GET <url>?name=&ip=&token=&proto=&user_agent=&session_id=`.
     - Resposta 2xx libera; qualquer outra nega.
     - A resposta fica guardada por `cache_seconds` (padrão 60).
     - Cabeçalhos opcionais na resposta: `X-AuthDuration`, `X-UserId`, `X-Max-Sessions` e `X-Unique`.
     - Backend fora do ar nega, a menos que `allow_on_error` esteja ligado.
  4. **Limite de sessões do canal no servidor** (`max_sessions`).
- **Token em todos os pedidos.** O worker repete o `?token=` da playlist em cada segmento e chave, como o Flussonic faz. Assim o player só precisa recebê-lo uma vez.
- **EPG:**
  - Cada canal pode apontar para um arquivo XMLTV (`.xml` ou `.xml.gz`) e para o ID do canal dentro dele.
  - O controller importa os guias a cada 6 h; cada URL é baixada uma vez, mesmo quando vários canais a usam.
  - O guia guarda de 7 dias para trás a 14 dias para frente. Programas que terminaram há mais de 30 dias são apagados.
  - O painel pode forçar a importação, e a API exporta tudo em `GET /epg.xml`.
  - Os programas que já passaram abrem o catch-up do DVR.
- **Áudio:** o pipeline passa a manter todas as faixas de áudio da entrada (idiomas, audiodescrição), e não só a primeira.
- **Painel:** a página do canal ganha as oito abas. Overview continua igual, e cada aba salva só a sua parte do canal.

## Consequências

- **Mais fácil:**
  - integrar o middleware de assinantes;
  - ver e derrubar espectadores;
  - limitar acessos por rede;
  - oferecer guia e catch-up por programa;
  - contar espectadores de HLS (antes só o MPEG-TS era contado).
- **Mais difícil:**
  - **Carga no worker:** os pedidos HLS passam pelo worker, que gasta CPU com isso. Ainda não foi medido.
  - **Cache do nginx:** só pode ser usado com cuidado, porque o token entra na URL.
  - **Sessões só na memória:** somem quando o worker reinicia e não ficam num histórico central.
  - **Limite por canal:** o limite de sessões de um canal (`max_sessions`) conta as sessões no servidor que roda o canal, que é um só.
  - **Limite por assinante no cluster:** o `X-Max-Sessions` vale para o cluster todo, de forma aproximada. Cada servidor publica no NATS, a cada 5 s, quantas sessões abertas cada assinante com limite tem, e soma o que os outros publicaram. Um assinante pode passar do limite por até um intervalo, se abrir sessões em dois servidores quase juntas. Um servidor que some deixa de contar depois de 15 s, então uma queda de rede nunca bloqueia assinantes. O `X-Unique` continua valendo só no servidor.
- **Ainda não feito:**
  - histórico de sessões no banco;
  - legendas e teletexto;
  - ABR (várias qualidades);
  - saídas DASH, RTMP, SRT e WebRTC.

## Referências

- [api.md](../api.md), seção 14.6; [deployment.md](../deployment.md), seção 7.
