# 0008 — DRM: HLS AES-128 com token de reprodução

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

O operador precisa proteger canais. Os players são variados: set-top boxes e apps IPTV, navegador e Android, Apple (iOS, tvOS, Safari) e Smart TVs.

O único método que todos eles tocam é o HLS AES-128. Os DRMs comerciais exigem outras coisas:

- **Widevine e PlayReady:** pedem fMP4/CMAF.
- **FairPlay:** pede SAMPLE-AES.
- **Os três:** precisam de um provedor de chaves (EZDRM, BuyDRM, Irdeto, Verimatrix…) via CPIX/SPEKE.

Hoje a saída HLS usa segmentos MPEG-TS. O DVR guarda esses segmentos em arquivos de uma hora ([ADR 0006](0006-dvr-local-no-servidor-de-midia.md)).

## Decisão

- **Por canal:** `drm.enabled`, `drm.method` (só `aes-128`) e `drm.key_rotation_minutes` (padrão 60, entre 1 e 1440). No editor de texto: `drm aes-128 rotate=1h;` ou `drm off;`.
- **Criptografia na entrega:** o empacotador continua gravando HLS em claro, mas numa pasta que o nginx não publica (`<HLS_DIR>/.drm/<canal>`). O worker cifra cada segmento quando ele é pedido:
  - AES-128-CBC, PKCS#7;
  - IV igual ao número de sequência da mídia, que é o padrão do HLS quando o `#EXT-X-KEY` não traz IV.

  O nginx guarda em cache os segmentos já cifrados, que são iguais para todos os espectadores.
- **Chaves:** derivadas por HMAC-SHA256 a partir de um segredo comum a todos os Media Nodes (`NEXOSTREAM_DRM_KEY_FILE`), do canal e do início do período de chave. Não existe banco de chaves. Qualquer nó, inclusive o DVR anos depois, recalcula a mesma chave.
- **Período da chave:** é calculado pelo servidor a partir do horário do segmento, e o cliente não consegue escolhê-lo. Assim, uma chave vazada só abre o seu período.
- **Token de reprodução:** a playlist e a chave exigem `?token=`. O worker repete o token nas URIs de chave da playlist, então o player só precisa recebê-lo uma vez.
  - Formato: `<expira>.hex(HMAC-SHA256(segredo, "nexostream-play/v1\n" + canal + "\n" + <expira>))`.
  - O segredo é `NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE`, igual nos nós e na API.
  - O middleware da operadora pode assinar tokens sozinho. A API também gera tokens: `POST /channels/{id}/playback-token`, que o painel usa nas prévias.
  - O servidor não entrega chaves de períodos além do próximo. Assim, quem tem o token vencendo não consegue guardar chaves do futuro.
- **DVR:** em canais protegidos, a playlist de timeshift e catch-up exige o token. Cada peça é servida cifrada em `enc/<hora>/<offset>.ts?s=<sequência>`, e os arquivos de hora em claro deixam de ser servidos. O formato em disco não muda: as horas continuam tocando no VLC para quem tem acesso ao servidor.
- **Falha fechada:**
  - Sem os segredos, o nó responde `503` para canais protegidos e nunca entrega o conteúdo em claro.
  - Ligar o DRM apaga a pasta HLS em claro que estava publicada.
  - A saída MPEG-TS por HTTP, que não é cifrada, é recusada junto com o DRM. A saída UDP continua permitida, porque fica na rede interna.
- **Provedor comercial depois:** as chaves vêm da interface `drm.KeyProvider`. Para Widevine, PlayReady e FairPlay, será preciso:
  - outro `KeyProvider` (CPIX/SPEKE) com cache de chaves;
  - empacotamento fMP4/CMAF;
  - licenças do provedor.

  Nada disso foi implementado.

## Consequências

- **Mais fácil:**
  - proteger qualquer canal sem mudar os players;
  - integrar o middleware de assinantes só com o segredo dos tokens;
  - o DVR e a troca de nó continuam funcionando sem sincronizar chaves.
- **Mais difícil:**
  - **Proteção limitada:** o AES-128 não é DRM de estúdio. Um cliente autorizado consegue obter a chave e decifrar o período.
  - **Mais trabalho no nó:** os segmentos de canais protegidos passam pelo worker, que gasta CPU para cifrar e depende do cache do nginx para não cifrar o mesmo segmento várias vezes.
  - **Segredo único:** trocar o segredo das chaves muda todas as chaves de uma vez. O DVR continua tocando, porque é cifrado na entrega, mas os players com chaves antigas em cache precisam recarregar a playlist.
  - **Sem contagem de espectadores:** o token não identifica o assinante.
- **Não comprovado:**
  - **Carga:** custo de CPU com muitos canais protegidos e espectadores, sem benchmark registrado.
  - **Aparelhos:** reprodução em set-top boxes e Smart TVs reais. Por enquanto só foram testados o FFmpeg e a decifragem conforme a especificação.

## Referências

- RFC 8216 (HLS), seções 4.3.2.4 (EXT-X-KEY) e 5.2 (IV).
- [api.md](../api.md), seção 14.5; [deployment.md](../deployment.md), seção 7.
