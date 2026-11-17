# API

Referência da API de gerenciamento do NexoStream.

> **Status:** implementada no MVP (`control-plane/api`), exceto as rotas de gravação do DVR, que respondem `501` até a Fase 5. Rotas, campos e formatos ainda podem mudar até o fim da Fase 4. A fonte oficial é o OpenAPI em `/api/v1/openapi.json`. As diferenças e os acréscimos em relação a esta proposta estão na [seção 14](#14-implementação-atual).

## Sumário

1. [Convenções](#1-convenções)
2. [Autenticação](#2-autenticação)
3. [Perfis de acesso (RBAC)](#3-perfis-de-acesso-rbac)
4. [Erros](#4-erros)
5. [Paginação e filtros](#5-paginação-e-filtros)
6. [Canais](#6-canais)
7. [Fontes](#7-fontes)
8. [Perfis de transcodificação](#8-perfis-de-transcodificação)
9. [Workers](#9-workers)
10. [DVR](#10-dvr)
11. [Eventos e status em tempo real](#11-eventos-e-status-em-tempo-real)
12. [Auditoria](#12-auditoria)
13. [Saídas de mídia](#13-saídas-de-mídia)
14. [Implementação atual](#14-implementação-atual)

---

## 1. Convenções

| Item | Convenção |
| --- | --- |
| Base | `https://<host>/api/v1` |
| Formato | JSON (`Content-Type: application/json`), UTF-8 |
| Campos | `snake_case`, em inglês |
| Datas | ISO 8601 em UTC (`2026-11-16T14:00:00Z`) |
| Identificadores | Texto (`canal-001`) definido no cadastro ou gerado pela API |
| Versionamento | Na URL (`/api/v1`); mudanças incompatíveis geram `/api/v2` |
| Rastreamento | Toda resposta traz o cabeçalho `X-Request-Id` |
| Idempotência | `POST` de criação aceita o cabeçalho `Idempotency-Key` |
| Transporte | Somente HTTPS |

## 2. Autenticação

### 2.1 Usuários (painel)

```http
POST /api/v1/auth/token
Content-Type: application/json

{
  "username": "operador1",
  "password": "********"
}
```

```json
{
  "access_token": "eyJ...",
  "token_type": "Bearer",
  "expires_in": 900,
  "refresh_token": "def..."
}
```

- O token de acesso expira em 15 minutos e é renovado com `POST /api/v1/auth/refresh`. O token de renovação **vale uma vez**: a renovação devolve um novo par, e o antigo deixa de funcionar. Se um token de renovação já usado for apresentado de novo, é sinal de que foi copiado: a API responde `401 token_reused`, encerra toda a sessão (a família de tokens, inclusive o mais novo) e registra `auth.refresh_reuse` na auditoria. Quem perde a resposta de uma renovação precisa entrar de novo.
- Todas as outras rotas exigem `Authorization: Bearer <access_token>`.
- `POST /api/v1/auth/logout` encerra todas as sessões do usuário. Trocar a senha ou o perfil de um usuário faz o mesmo. Os tokens emitidos antes disso, inclusive o de renovação, deixam de valer.
- O login é limitado por IP e também por nome de usuário (metade da taxa por IP). Os contadores ficam no Redis, então o limite vale para o conjunto das instâncias da API (a janela é de 1 minuto). Se o Redis cair, cada instância passa a contar sozinha, sem atraso nem recusa de login. As falhas ficam na auditoria como `auth.login_failed`.
- O IP do cliente só é lido de `X-Real-IP` ou `X-Forwarded-For` quando a conexão vem de loopback (o nginx local) ou de um endereço em `NEXOSTREAM_TRUSTED_PROXIES`. Fora disso o cabeçalho é ignorado, para ninguém forjar o IP e escapar do limite de login.

**Bloqueio de conta.** Depois de 5 falhas seguidas (senha errada), a conta fica bloqueada por 5 minutos. A cada novo múltiplo de 5 falhas, o tempo dobra, até 1 hora. Tentativas durante o bloqueio são recusadas sem estender o tempo, e até a senha certa é recusada. A resposta é a mesma de uma senha errada (`401 invalid_credentials`), para não revelar se a conta existe ou está bloqueada. Um login bem-sucedido zera a contagem. Um administrador desbloqueia com `PATCH /users/{id}` e `{"unlock": true}`. Ajustes: `NEXOSTREAM_LOCKOUT_THRESHOLD` (negativo desliga), `NEXOSTREAM_LOCKOUT_BASE` e `NEXOSTREAM_LOCKOUT_MAX`. Auditoria: `auth.account_locked` e `auth.login_locked`.

### 2.2 Tokens de integração

- Criados por um `admin` em `POST /api/v1/api-tokens`, com escopos e validade.
- O valor do token aparece uma única vez, na criação. O banco guarda apenas o hash.
- Escopos sugeridos: `channels:read`, `channels:write`, `channels:operate`, `dvr:read`, `workers:read`, `workers:write`, `audit:read`.

## 3. Perfis de acesso (RBAC)

| Perfil | Permissões |
| --- | --- |
| `admin` | Acesso total, incluindo usuários, tokens e configurações gerais |
| `operator` | Operar canais, fontes, DVR e workers; sem gestão de usuários |
| `viewer` | Somente leitura de status, métricas e eventos |
| `integration` | Token de serviço com os escopos concedidos |

Toda ação de escrita gera um registro de auditoria (ver [seção 12](#12-auditoria)).

## 4. Erros

Formato único para todas as respostas de erro:

```json
{
  "error": {
    "code": "channel_not_found",
    "message": "Canal 'canal-999' não encontrado.",
    "request_id": "01JD3K8T2X..."
  }
}
```

| HTTP | Quando |
| --- | --- |
| `400` | Requisição malformada |
| `401` | Sem autenticação ou token expirado |
| `403` | Sem permissão para a ação |
| `404` | Recurso inexistente |
| `409` | Conflito (por exemplo, ID já existente ou canal em transição de estado) |
| `422` | Dados válidos em formato, mas inválidos em regra de negócio |
| `429` | Limite de requisições excedido |
| `500` | Erro interno |
| `503` | Dependência indisponível (banco, NATS) |

## 5. Paginação e filtros

Listas usam paginação por cursor:

```http
GET /api/v1/channels?limit=50&cursor=eyJpZCI6ImNhbmFsLTA1MCJ9&state=RUNNING&group=Noticias
```

```json
{
  "items": [ ... ],
  "next_cursor": "eyJpZCI6ImNhbmFsLTEwMCJ9"
}
```

`next_cursor` vem como `null` na última página. O valor máximo de `limit` é 200.

## 6. Canais

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/channels` | `channels:read` | Listar canais |
| `POST` | `/channels` | `channels:write` | Criar canal |
| `GET` | `/channels/{id}` | `channels:read` | Detalhar canal |
| `PATCH` | `/channels/{id}` | `channels:write` | Atualizar canal |
| `DELETE` | `/channels/{id}` | `channels:write` | Remover canal (precisa estar `STOPPED`) |
| `POST` | `/channels/{id}/start` | `channels:operate` | Iniciar |
| `POST` | `/channels/{id}/stop` | `channels:operate` | Parar |
| `POST` | `/channels/{id}/restart` | `channels:operate` | Reiniciar o pipeline |
| `POST` | `/channels/{id}/switch-source` | `channels:operate` | Trocar a fonte manualmente |
| `GET` | `/channels/{id}/status` | `channels:read` | Estado atual |
| `GET` | `/channels/{id}/events` | `channels:read` | Histórico de eventos |

### 6.1 Criar canal

```http
POST /api/v1/channels
Authorization: Bearer <token>
Content-Type: application/json

{
  "id": "canal-001",
  "name": "Canal Exemplo",
  "group": "Noticias",
  "enabled": true,
  "mode": "passthrough",
  "profiles": [],
  "sources": [
    { "priority": 1, "url": "udp://239.1.1.1:1234", "interface": "eno2" },
    { "priority": 2, "url": "udp://239.1.2.1:1234", "interface": "eno3" }
  ],
  "outputs": { "hls": true, "mpegts_http": true },
  "dvr": { "enabled": true, "retention_hours": 72 },
  "failover": {
    "no_packets_seconds": 3,
    "confirm_window_seconds": 5,
    "return_to_primary": true,
    "primary_stable_seconds": 60
  }
}
```

Resposta `201 Created` com o canal completo e o cabeçalho `Location: /api/v1/channels/canal-001`.

| Campo | Tipo | Obrigatório | Observação |
| --- | --- | --- | --- |
| `id` | string | não | Gerado se ausente; `[a-z0-9-]`, até 64 caracteres |
| `name` | string | sim | Nome exibido |
| `group` | string | não | Organização no painel e políticas de DVR |
| `enabled` | boolean | não | Padrão `true`; com `true`, o canal é iniciado |
| `mode` | string | não | `auto` (padrão), `passthrough` ou `transcode` |
| `profiles` | string[] | condicional | Obrigatório quando `mode = transcode` |
| `sources` | object[] | sim | Pelo menos uma fonte |
| `outputs` | object | não | Padrão: HLS ativo |
| `dvr` | object | não | Padrão: desativado. Em canal ABR, `all_qualities: true` grava todas as qualidades (padrão: só a mais alta; o disco cresce com a soma delas) |
| `failover` | object | não | Padrão: parâmetros globais |

### 6.2 Status do canal

```http
GET /api/v1/channels/canal-001/status
```

```json
{
  "channel_id": "canal-001",
  "state": "ON_BACKUP",
  "since": "2026-11-16T14:03:12Z",
  "worker_id": "media-02",
  "active_source": { "priority": 2, "url": "udp://239.1.2.1:1234" },
  "processing": "passthrough",
  "input": {
    "bitrate_bps": 4120000,
    "video": { "codec": "h264", "width": 1920, "height": 1080, "fps": 25 },
    "audio": [ { "codec": "aac", "channels": 2, "sample_rate": 48000 } ],
    "cc_errors_last_minute": 0,
    "last_packet_at": "2026-11-16T14:10:41Z"
  },
  "outputs": {
    "hls": "https://origin.exemplo.local/canal-001/index.m3u8",
    "mpegts_http": "https://origin.exemplo.local/canal-001/stream.ts"
  },
  "viewers": 37
}
```

### 6.3 Trocar fonte manualmente

```http
POST /api/v1/channels/canal-001/switch-source
Content-Type: application/json

{ "priority": 1 }
```

Resposta `202 Accepted`. A troca é assíncrona; o resultado aparece no status e nos eventos do canal.

## 7. Fontes

| Método | Rota | Descrição |
| --- | --- | --- |
| `GET` | `/channels/{id}/sources` | Listar fontes do canal |
| `POST` | `/channels/{id}/sources` | Adicionar fonte |
| `PATCH` | `/channels/{id}/sources/{source_id}` | Alterar URL, interface ou prioridade |
| `DELETE` | `/channels/{id}/sources/{source_id}` | Remover fonte (não pode ser a única) |
| `POST` | `/sources/probe` | Analisar uma URL sem cadastrá-la (codecs, bitrate) |

## 8. Perfis de transcodificação

| Método | Rota | Descrição |
| --- | --- | --- |
| `GET` | `/transcode-profiles` | Listar perfis |
| `POST` | `/transcode-profiles` | Criar perfil |
| `PATCH` | `/transcode-profiles/{id}` | Atualizar perfil |
| `DELETE` | `/transcode-profiles/{id}` | Remover perfil sem uso |

```json
{
  "id": "hd",
  "video": {
    "codec": "h264",
    "profile": "high",
    "width": 1280,
    "height": 720,
    "bitrate_bps": 3000000,
    "max_bitrate_bps": 3000000,
    "gop_seconds": 2,
    "deinterlace": "auto"
  },
  "audio": { "codec": "aac", "bitrate_bps": 128000, "sample_rate": 48000, "channels": 2 },
  "encoder": "auto"
}
```

`encoder` aceita `auto`, `cpu`, `nvenc` ou `qsv`. Com `auto`, o scheduler usa a aceleração disponível no nó escolhido.

## 9. Workers

| Método | Rota | Descrição |
| --- | --- | --- |
| `GET` | `/workers` | Listar nós, estado e recursos |
| `GET` | `/workers/{id}` | Detalhar nó e canais atribuídos |
| `POST` | `/workers/{id}/drain` | Parar de receber canais e migrar os atuais |
| `POST` | `/workers/{id}/undrain` | Voltar a receber canais |

```json
{
  "id": "media-02",
  "state": "ONLINE",
  "last_heartbeat_at": "2026-11-16T14:10:40Z",
  "version": "0.1.0",
  "capabilities": { "gpu": "nvidia", "encoders": ["cpu", "nvenc"] },
  "resources": {
    "cpu_percent": 54.2,
    "memory_used_bytes": 41234567168,
    "memory_total_bytes": 137438953472,
    "gpu_percent": 38.0,
    "network_in_bps": 980000000,
    "network_out_bps": 1450000000
  },
  "channels": 212
}
```

Estados do nó: `ONLINE`, `DRAINING`, `OFFLINE`.

## 10. DVR

| Método | Rota | Descrição |
| --- | --- | --- |
| `GET` | `/dvr/{channel_id}/recordings?from=&to=` | Intervalos gravados disponíveis |
| `GET` | `/dvr/{channel_id}/playlist.m3u8?from=&to=` | Playlist HLS de catch-up do intervalo |
| `GET` | `/dvr/policies` | Listar políticas de gravação |
| `PATCH` | `/dvr/policies/{id}` | Alterar retenção ou canais incluídos |
| `GET` | `/dvr/storage` | Uso de espaço por volume |

```json
{
  "channel_id": "canal-001",
  "ranges": [
    { "from": "2026-11-13T14:00:00Z", "to": "2026-11-16T09:12:30Z" },
    { "from": "2026-11-16T09:13:05Z", "to": "2026-11-16T14:10:36Z" }
  ]
}
```

Lacunas entre os intervalos indicam períodos sem sinal ou sem gravação.

## 11. Eventos e status em tempo real

### 11.1 Stream para o painel

```http
GET /api/v1/stream/events
Accept: text/event-stream
```

Server-Sent Events com mudanças de estado de canais e workers:

```text
event: channel.state_changed
data: {"channel_id":"canal-001","from":"DEGRADED","to":"SWITCHING","at":"2026-11-16T14:03:10Z","reason":"no_packets"}
```

### 11.2 Tipos de evento

| Evento | Quando |
| --- | --- |
| `channel.state_changed` | Qualquer mudança de estado do canal |
| `channel.source_switched` | Troca de fonte (automática ou manual) |
| `channel.pipeline_restarted` | Reinício do pipeline |
| `channel.input_changed` | Mudança de codec, resolução ou FPS na fonte |
| `worker.online` / `worker.offline` | Nó entrou ou saiu do cluster |
| `worker.channels_reassigned` | Canais realocados após falha ou drain |
| `dvr.storage_warning` | Volume do DVR acima do limite |

### 11.3 Webhooks [Proposta para fase futura]

Integrações poderão registrar URLs para receber esses mesmos eventos por `POST`, com assinatura HMAC no cabeçalho `X-NexoStream-Signature`.

## 12. Auditoria

```http
GET /api/v1/audit-log?from=2026-11-16T00:00:00Z&actor=operador1
```

```json
{
  "items": [
    {
      "at": "2026-11-16T14:05:00Z",
      "actor": "operador1",
      "actor_type": "user",
      "action": "channel.switch_source",
      "target": "canal-001",
      "source_ip": "10.10.0.15",
      "request_id": "01JD3K8T2X...",
      "details": { "priority": 1 }
    }
  ],
  "next_cursor": null
}
```

Registros de auditoria não podem ser alterados nem apagados pela API.

## 13. Saídas de mídia

As saídas de mídia não passam pela API. Elas são servidas pela origem HTTP (nginx) dos Media Nodes ou pela camada de distribuição:

| Saída | URL de exemplo |
| --- | --- |
| HLS ao vivo | `https://<origem>/<channel_id>/index.m3u8` |
| MPEG-TS sobre HTTP | `https://<origem>/<channel_id>/stream.ts` |
| Catch-up | `https://<host>/api/v1/dvr/<channel_id>/playlist.m3u8?from=&to=` |
| Exportação em MP4 | `https://<servidor de mídia>/dvr/<channel_id>/archive-<de>-<duração>.mp4` |

**Exportação em MP4:** `<de>` é o início em segundos Unix e `<duração>` vai de 1 s a 2 h. O servidor copia os pacotes gravados, sem recodificar, e entrega um MP4 fragmentado como download. A granularidade é a do segmento gravado, e as respostas trazem `X-Export-From` e `X-Export-To` com o trecho que de fato saiu. Se houver um buraco na gravação, sai só o trecho contínuo a partir do início; peça o seguinte em outra chamada (`recordings.json` mostra os intervalos). No máximo 2 exportações rodam ao mesmo tempo em cada servidor (as outras recebem `503` com `Retry-After`), e um cliente que não leia o arquivo por 30 s é desconectado. Canais com DRM respondem `403`: o MP4 sairia em claro. As regras de autorização e a contagem de sessões valem como no resto de `/dvr/`. Em canal ABR com `all_qualities`, use `/dvr/<channel_id>/v<N>/archive-...` para uma qualidade específica.

As saídas passam pelo servidor de mídia, que conta as sessões de reprodução. Canais com DRM exigem um token na URL (`?token=`), conforme a [seção 14.5](#145-drm-e-token-de-reprodução). Canais com autorização aplicam backend, listas de IP e limite de sessões, conforme a [seção 14.6](#146-sessões-autorização-e-epg).

## 14. Implementação atual

### 14.1 Rotas acrescentadas

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/auth/me` | autenticado | Identidade, perfil e escopos do chamador |
| `POST` | `/auth/logout` | autenticado (usuário) | Revoga as sessões do usuário; responde `204` |
| `GET` | `/events` | `channels:read` | Eventos de todos os canais e workers (`type`, `channel_id`, `worker_id`, `from`, `to`) |
| `GET` | `/system/summary` | `channels:read` | Resumo do dashboard: canais por estado, workers, canais com problema, eventos recentes |
| `GET`/`POST` | `/users` | `users:admin` | Listar e criar usuários (`admin`, `operator`, `viewer`; senha de 10 a 72 caracteres, com pelo menos 5 diferentes e sem ser só uma palavra comum) |
| `PATCH` | `/users/{id}` | `users:admin` | Alterar perfil, senha ou desativar (efeito imediato) |
| `GET`/`POST` | `/api-tokens` | `tokens:admin` | Listar e criar tokens; o valor `nxs_…` aparece só na criação |
| `DELETE` | `/api-tokens/{id}` | `tokens:admin` | Revogar token |
| `GET`/`PATCH` | `/transcode-profiles/{id}` | `channels:read` / `channels:write` | Detalhar e atualizar perfil (merge parcial) |
| `GET` | `/healthz`, `/readyz` | público | Liveness e prontidão (banco e cache) |

### 14.2 Comportamentos definidos

- **`start` e `stop`** alteram o estado desejado (`enabled`) e respondem `202`; o controller inicia ou para o canal no worker. `restart` e `switch-source` são enviados direto ao worker atribuído e respondem `409 channel_not_assigned` se o canal ainda não tiver worker.
- **`switch-source` para um backup** suspende o retorno automático à principal até nova troca manual ou reinício.
- **Status sem worker:** canal ativo sem status recente aparece como `STARTING` com `reason: awaiting_worker`.
- **Filtros de lista:** `GET /channels` aceita `state`, `group`, `enabled`, `worker_id`, `processing` e `q` (busca por id ou nome).
- **Validação:** campos desconhecidos geram `400`. Regras de negócio geram `422 invalid_channel`, com `details` por campo (por exemplo, `sources[0].url`).
- **Limites:** até 8 fontes, com os esquemas `udp`, `rtp`, `srt`, `rtmp`, `rtmps`, `rtsp`, `http` e `https`; perfis de transcodificação conforme a seção 14.7 (ABR).
- **Logo (overlay):** `overlay: {"enabled": true, "image": "logo.png", "position": "top-right", "margin": 24, "width": 200, "opacity": 0.8}` grava o logo no vídeo, em todas as qualidades do ABR. Exige `mode: transcode` (em passthrough o vídeo é copiado, sem decodificar). `image` é só o nome do arquivo (PNG ou JPG), nunca um caminho: o servidor de mídia o procura na sua pasta de logos (`NEXOSTREAM_OVERLAY_DIR`, padrão `/var/lib/nexostream/overlays`). Se o arquivo não existir num servidor, o canal continua no ar sem o logo e o motivo vai para o log. Mudar o logo reinicia o pipeline. No editor de texto: `overlay logo.png position=bottom-left margin=20 width=160 opacity=0.8;` ou `overlay off;`.
- **Receber de um encoder (publish):** o servidor de mídia escuta e o encoder (OBS, vMix, ffmpeg, encoders de hardware) envia. Duas formas:
  - **SRT, recomendado:** `srt://0.0.0.0:9000?mode=listener&passphrase=<10 a 79 caracteres>`. A API **exige** a `passphrase`: ela autentica quem envia e **cifra** o fluxo. O encoder usa `srt://servidor:9000?mode=caller&passphrase=...`. Com senha errada ou ausente, o SRT recusa a conexão (`BADSECRET`/`UNSECURE`).
  - **RTMP:** `rtmp://0.0.0.0:1935/<app>/<chave>?mode=listener`. A API exige uma chave de pelo menos 16 caracteres. O encoder publica em `rtmp://servidor:1935/<app>` com a chave como "stream key". O servidor de mídia confere o app e a chave (em tempo constante) **antes** de aceitar qualquer vídeo; com qualquer um deles errado, responde `NetStream.Publish.BadName` e fecha a conexão. Um encoder por vez: outro que tente enquanto o primeiro transmite é recusado. Conexões que não chegam a publicar (health checks, scanners) são fechadas sem afetar o canal. **O RTMP não é cifrado**: a chave trafega em claro, então use-o em rede de confiança ou atrás de um proxy TLS; na internet, prefira SRT. `rtmps` em modo listener não existe.
- **Segredos nas respostas:** senhas e chaves guardadas no canal (`passphrase` do SRT, usuário e senha do RTSP, chave de stream do RTMP, tokens nas URLs de push, de backend de autorização e de guia EPG) só aparecem a quem tem o escopo `channels:write`. Quem só lê (perfil `viewer` ou token de integração só de leitura) recebe os valores mascarados como `••••` em `/channels`, `/channels/{id}`, `/channels/{id}/sources` e na exportação `/config`. O status que o servidor de mídia publica, os eventos, os logs e a auditoria também saem mascarados. O que está guardado não muda.
- **Paginação de eventos e auditoria:** do mais novo para o mais antigo; `next_cursor` é opaco e deve ser repassado em `cursor`.
- **Limites:** login limitado por IP (`429`; padrão de 10 tentativas por minuto, ajustável em Configurações › Segurança); corpo até 1 MB.
- **Idempotência:** `Idempotency-Key` em `POST /channels` repete a mesma resposta por 24 h, por usuário. O registro fica no Redis, então vale entre todas as instâncias da API. Sem Redis (modo dev) ou durante uma queda dele, cada instância usa a própria memória.

### 14.3 Stream de eventos

`GET /stream/events` aceita `types=` (lista separada por vírgulas) e `status=true`. Com `status=true`, o stream também envia `event: channel.status` com o status completo do canal a cada mudança e a cada 5 s, o que alimenta o painel em tempo real. O painel consome o stream com `fetch` e o cabeçalho `Authorization`, sem token na URL.

### 14.4 Configurações do sistema

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/settings` | `settings:admin` | Valores atuais, padrões e `saved` (falso enquanto valem os `.env`) |
| `PUT` | `/settings` | `settings:admin` | Salva todas as configurações do sistema; `422 invalid_settings` com `details` por campo |
| `GET` | `/settings/defaults` | `channels:read` | O que os canais novos recebem: failover, retenção e armazenamento do DVR, perfil |
| `GET` | `/settings/servers` | `settings:admin` | Servidores de mídia com seus ajustes e o runtime resultante |
| `PUT` | `/settings/servers/{id}` | `settings:admin` | Interfaces de ingestão, pasta do DVR e URLs públicas de um servidor (vazio = `.env`) |

- No editor de texto, `GET /config` inclui os blocos `settings { }` e `server <id> { }` para quem tem `settings:admin`.
- `POST /config/validate` e `POST /config/apply` informam `settings` (`update`, `unchanged` ou vazio) e `servers` (plano de criação e alteração). Os blocos de servidor nunca são removidos pelo `prune`.
- **Whitelabel:** `system_name`, `brand_tagline`, `brand_color` (`#RRGGBB`), `brand_logo` (data URL PNG, SVG, JPEG ou WebP de até 256 KB) e `brand_login_message`.
  - `GET /branding` é público, porque a tela de login usa esses dados antes de alguém entrar. Ele responde `name`, `tagline`, `color`, `logo` e `login_message`.
  - No editor de texto, os campos ficam em `name`, `tagline`, `brand_color "#rrggbb"` e `login_message`. O logo fica só no painel: aplicar um texto sem logo mantém o logo atual.
- O controller entrega as mudanças aos servidores com o comando `configure`. O modelo está no [ADR 0007](adr/0007-configuracoes-gerenciadas-pelo-painel.md).

### 14.5 DRM e token de reprodução

O modelo está no [ADR 0008](adr/0008-drm-aes-128-com-token.md).

**Configuração no canal.** O canal recebe o campo `drm`:

```json
"drm": { "enabled": true, "method": "aes-128", "key_rotation_minutes": 60 }
```

- `method` vazio vira `aes-128`; `key_rotation_minutes` vazio vira 60, com limite de 1 a 1440.
- Com `enabled: false`, os demais campos são apagados.
- Com DRM ligado, `outputs.mpegts_http` gera `422`, porque o MPEG-TS por HTTP não é criptografado. Também é preciso ter HLS ou DVR ligado.

**Rota de token.**

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `POST` | `/channels/{id}/playback-token` | `channels:read` | Gera um token. Corpo opcional: `{"ttl_seconds": 21600}` (de 60 s a 7 dias; padrão 6 h). Responde `token`, `expires_at`, `hls_url` e `dvr_url`, estes dois já com `?token=`. Canal sem DRM: `409 channel_not_protected`. API sem a chave: `503 drm_not_configured` |

**Assinar tokens fora da API.** O middleware da operadora pode assinar os tokens sozinho, usando o mesmo segredo de `NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE`:

```text
expira = tempo Unix em segundos
token  = expira + "." + hex(HMAC-SHA256(segredo, "nexostream-play/v1\n" + channel_id + "\n" + expira))
```

**O que o servidor de mídia serve num canal protegido:**

| URL | Token | Conteúdo |
| --- | --- | --- |
| `/<canal>/index.m3u8?token=` | sim | Playlist ao vivo com `#EXT-X-KEY` |
| `/<canal>/enc/seg_N.ts` | não | Segmento cifrado |
| `/<canal>/keys/<período>.key?token=` | sim | Chave de 16 bytes |
| `/dvr/<canal>/index.m3u8?from=&to=&token=` | sim | Playlist de timeshift ou catch-up |
| `/dvr/<canal>/index-<de>-<duração>.m3u8?token=` | sim | Playlist de timeshift ou catch-up no formato Flussonic |
| `/dvr/<canal>/enc/<hora>/<offset>.ts?s=` | não | Peça do DVR cifrada |
| `/dvr/<canal>/keys/…` | sim | Chave |

**Respostas de erro:**

- Sem token, ou com token inválido ou vencido: `403`.
- Nó sem os segredos: `503`.
- Os segmentos e as horas do DVR em claro nunca são servidos.

### 14.6 Sessões, autorização e EPG

O modelo está no [ADR 0009](adr/0009-sessoes-autorizacao-e-epg.md). O canal ganhou os campos `auth` e `epg`:

```json
"auth": { "enabled": true, "backend_url": "https://middleware/auth", "allow_ips": ["10.0.0.0/8"],
          "deny_ips": ["10.9.0.0/16"], "max_sessions": 500, "cache_seconds": 60, "allow_on_error": false },
"epg":  { "url": "https://epg.exemplo/guia.xml.gz", "xmltv_id": "canal.br" }
```

**Rotas**

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/channels/{id}/sessions` | `channels:read` | Sessões abertas e as encerradas recentemente, consultadas no worker do canal |
| `DELETE` | `/channels/{id}/sessions/{session_id}` | `channels:operate` | Encerra a sessão e a recusa por 2 min (auditoria `session.kill`) |
| `GET` | `/sessions` | `channels:read` | Sessões abertas em todos os workers online |
| `GET` | `/channels/{id}/epg?from=&to=` | `channels:read` | Programas (padrão: de 12 h atrás até 24 h à frente) e o resultado da última importação |
| `POST` | `/channels/{id}/epg/refresh` | `channels:write` | Importa o guia agora (auditoria `epg.refresh`) |
| `GET` | `/epg.xml?hours=48` | `channels:read` | Exporta o guia de todos os canais em XMLTV |

**Backend de autorização**

O servidor de mídia chama o backend a cada nova sessão:

```text
GET <backend_url>?name=<canal>&ip=<ip>&token=<token>&proto=hls|mpegts|dvr&user_agent=<player>&session_id=<id>
```

- 2xx libera; qualquer outro status nega.
- **Cabeçalhos opcionais da resposta:**
  - `X-AuthDuration`: segundos até a próxima consulta;
  - `X-UserId`: assinante;
  - `X-Max-Sessions`: limite de sessões do assinante, somado entre todos os servidores de mídia (aproximado: os servidores trocam as contagens a cada 5 s);
  - `X-Unique: true`: derruba as outras sessões do assinante.
- **Quando o pedido é negado:** o servidor de mídia responde `403` com `X-Denied-Reason`. Os valores possíveis são `ip_denied`, `ip_not_allowed`, `backend_denied`, `backend_unreachable`, `max_sessions`, `user_max_sessions` e `closed_by_operator`.

O token do link da playlist é repetido em todos os segmentos e chaves.

`POST /channels/{id}/playback-token` também funciona para canais com `auth`: um token assinado pelo cluster libera sem consultar o backend, mas as listas de IP continuam valendo.

### 14.7 ABR, alertas, estatísticas, miniaturas e papéis dos servidores

- **ABR** ([ADR 0011](adr/0011-abr-com-um-decode.md)):
  - com `mode: transcode`, `profiles` aceita de 1 a 6 perfis distintos;
  - mais de um perfil em outro modo responde `422`;
  - o status do canal traz `pipeline.renditions`, com perfil, resolução, taxa e encoder de cada qualidade.
- **Miniatura:** o status traz `thumbnail_url`, que aponta para `/hls/<canal>/thumb.jpg` no servidor de mídia.
  - É um JPEG de 320 px do segmento mais recente, gerado no máximo a cada 10 s, só quando alguém pede.
  - Canais com DRM ou autorização exigem o token de reprodução do cluster.
- **Papéis dos servidores:** `PUT /settings/servers/{id}` aceita `roles`, com os valores `passthrough`, `transcode` e `dvr`.
  - Sem papéis, o servidor aceita qualquer canal.
  - Com papéis, o scheduler só manda canais compatíveis:
    - transcodificação precisa de `transcode`;
    - os demais modos precisam de `passthrough`;
    - DVR ligado também precisa de `dvr`.
  - No editor de texto: `server media-01 { roles transcode; }`.

**Rotas**

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/alerts` | `channels:read` | Alertas ativos (§14.3 da especificação), críticos primeiro, mais `counts` |
| `GET` | `/stats?keys=&prefix=&from=&to=` | `channels:read` | Séries por minuto, agregadas em cerca de 240 pontos; período de até 31 dias |
| `GET` | `/stats/top-channels?from=&to=&limit=` | `channels:read` | Canais por audiência (espectador-minuto) e pico de espectadores |

**Regras de alerta**

| Regra | Condição |
| --- | --- |
| `no_signal` | Canal em falha, ou sem pacotes há mais de 10 s |
| `on_backup` | No backup há mais de 10 min |
| `restarts` | Mais de 5 reinícios em 10 min |
| `cc_errors` | Mais de 100 erros CC por minuto |
| `worker_offline` | Servidor sem heartbeat |
| `dvr_disk` | Disco do DVR acima de 85% (crítico acima de 95%) ou com falha |
| `dvr_integrity` | Horas gravadas com problema nas últimas 24 h |
| `node_resources` | CPU ou GPU acima de 90% |

**Chaves das estatísticas**

O controller líder grava uma amostra por minuto, e cada amostra fica guardada 30 dias:

- `channels.total`, `channels.enabled`, `channels.up` e `channels.failed`;
- `viewers.total`, `input.bps` e `workers.online`;
- `worker:<id>:cpu`, `worker:<id>:mem`, `worker:<id>:net_in` e `worker:<id>:net_out`;
- `channel:<id>:viewers`, só para canais com espectadores.

**Eventos novos**

- `dvr.integrity_error`: uma hora gravada com índice, tamanho ou sincronismo MPEG-TS inconsistentes.
- `worker.encoder_failed`: um encoder de hardware falhou e ficou 30 min em quarentena.

### 14.8 Push, DASH, legendas e histórico de sessões

- **`outputs.dash`:** publica MPEG-DASH (fMP4) em `/<canal>/dash/index.mpd`.
  - Num canal ABR, todas as qualidades entram no mesmo conjunto de adaptação.
  - Junto com DRM responde `422`, porque o DASH não é criptografado.
  - O `?token=` do manifesto é repetido nos templates dos segmentos.
  - No editor de texto: `output dash;`.
- **`outputs.push`:** até 10 destinos `rtmp://`, `rtmps://` ou `srt://`.
  - Cada destino tem um FFmpeg próprio, alimentado pelo relay MPEG-TS do canal. Se cair, reconecta com espera de 1 s a 30 s, sem afetar o canal nem os outros destinos.
  - RTMP leva um vídeo e um áudio; SRT leva todas as trilhas.
  - O status do canal traz `pushes`: URL com a chave escondida, estado (`connecting`, `running` ou `retrying`), bytes enviados, reconexões e último erro.
  - No editor de texto: `push rtmp://…;`.
- **Legendas DVB e teletexto:**
  - São mantidas nas saídas MPEG-TS (HTTP, UDP e push SRT).
  - Ficam fora do HLS, do DASH e do RTMP, que não carregam esse formato.
  - O status mostra `input.subtitles` e `pipeline.subtitle_tracks`.
- **Histórico de sessões:** cada servidor de mídia envia as sessões encerradas em lotes (a cada 10 s), e os controllers gravam.
  - O envio é best effort: se nenhum controller estiver no ar, o lote se perde.
  - O histórico é guardado 30 dias.

| Método | Rota | Escopo | Descrição |
| --- | --- | --- | --- |
| `GET` | `/sessions/history?channel_id=&user_id=&ip=&from=&to=&limit=&cursor=` | `channels:read` | Sessões encerradas, da mais nova para a mais antiga, com `next_cursor` |

