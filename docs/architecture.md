# Arquitetura

Este documento descreve os componentes do NexoStream, como eles se comunicam e como um canal é processado do recebimento até a entrega.

> Trechos marcados com **[Proposta]** ainda precisam ser validados. A especificação de referência é [../../NexoStream_Projeto.md](../../NexoStream_Projeto.md).

## Sumário

1. [Visão geral](#1-visão-geral)
2. [Data Plane](#2-data-plane)
3. [Control Plane](#3-control-plane)
4. [Comunicação entre componentes](#4-comunicação-entre-componentes)
5. [Ciclo de vida de um canal](#5-ciclo-de-vida-de-um-canal)
6. [Estados do canal e failover](#6-estados-do-canal-e-failover)
7. [Cluster e scheduler](#7-cluster-e-scheduler)
8. [DVR, Timeshift e Catch-up](#8-dvr-timeshift-e-catch-up)
9. [Resiliência do próprio Control Plane](#9-resiliência-do-próprio-control-plane)
10. [Evolução prevista](#10-evolução-prevista)
11. [Implementação atual (MVP)](#11-implementação-atual-mvp)

---

## 1. Visão geral

A plataforma é dividida em dois domínios independentes:

- **Data Plane:** processa e transporta a mídia em tempo real, dentro de cada Media Node.
- **Control Plane:** guarda a configuração, decide onde cada canal roda, acompanha a saúde e expõe API e painel.

A regra principal é o isolamento: **se o Control Plane ficar indisponível, os canais já em execução continuam no ar.** O agente de cada nó mantém a última configuração recebida e segue supervisionando os pipelines locais.

```text
                 +---------------------------+
                 |        Painel Web         |
                 |     React / TypeScript    |
                 +-------------+-------------+
                               |
                               v
                 +---------------------------+
                 |       API / Control       |
                 |            Go             |
                 +------+----------+---------+
                        |          |
               +--------+--+   +---+---------+
               | PostgreSQL|   | Redis / NATS|
               +-----------+   +-------------+
                        |
                        v
              +-----------------------+
              |  Cluster Controller   |
              |  Workers / Scheduler  |
              +-----------+-----------+
                          |
             +------------+------------+
             |                         |
             v                         v
      +-------------+           +-------------+
      | Media Node 1|    ...    | Media Node N|
      | Ingest/Media|           | Ingest/Media|
      +------+------+           +------+------+
             |                         |
             v                         v
       HLS / MPEG-TS              HLS / MPEG-TS
             |                         |
             +------------+------------+
                          v
                  Aplicações clientes
```

## 2. Data Plane

Cada Media Node executa um **agente** (`nexostream-worker`) e os pipelines de mídia que ele supervisiona.

| Módulo | Pasta | Responsabilidade |
| --- | --- | --- |
| Ingestão | `media-engine/ingest` | Receber a fonte (UDP/MPEG-TS na v1) |
| Pipeline | `media-engine/pipeline` | Montar e controlar o pipeline GStreamer/FFmpeg de cada canal |
| Análise | `media-engine/pipeline` | Identificar codecs, resolução, FPS, bitrate e integridade |
| Transcodificação | `media-engine/transcoding` | Aplicar perfis de saída em CPU, NVENC ou Quick Sync |
| Empacotamento | `media-engine/packaging` | Gerar HLS e MPEG-TS (HTTP/UDP) |
| DVR | `media-engine/dvr` | Gravar segmentos, indexar e aplicar retenção |
| Health | `media-engine/health` | Medir continuidade, bitrate, timestamps e presença de áudio/vídeo |
| Agente | `data-plane/agent` | Receber comandos, supervisionar processos, enviar heartbeat e métricas |

### 2.1 Modelo de execução [Proposta]

- **Um pipeline por canal**, em processo separado. Uma falha em um canal não derruba os outros e o consumo de cada canal pode ser medido individualmente.
- O agente inicia, monitora e reinicia esses processos. Ele lê as estatísticas do pipeline (bytes recebidos, erros de continuidade, timestamps) e as transforma em métricas e eventos.
- Os segmentos HLS são gravados em disco local (`/var/lib/nexostream/hls/<canal>/`) e servidos por um servidor HTTP de origem (nginx). Assim, a entrega aos clientes não passa pelo processo do pipeline.

### 2.2 Decisão entre passthrough e transcodificação

```text
            Análise da fonte (probe)
                      |
        O vídeo e o áudio servem para
        a saída configurada?
           |                     |
          sim                   não
           |                     |
           v                     v
   Remux / Passthrough     Transcodificação
   (sem recodificar)       (perfis do canal)
```

Regras iniciais:

- H.264 + AAC → passthrough para HLS e TS.
- H.264 + MP2 ou AC-3 → copiar o vídeo e converter apenas o áudio para AAC quando a saída exigir compatibilidade ampla.
- MPEG-2 ou HEVC → transcodificar para H.264 quando os clientes não suportarem o codec de origem.
- O operador pode forçar `passthrough` ou `transcode` por canal.

## 3. Control Plane

| Componente | Pasta | Responsabilidade |
| --- | --- | --- |
| API | `control-plane/api` | REST para painel e integrações, autenticação, RBAC, auditoria |
| Controller | `control-plane/controller` | Estado desejado × estado real dos canais e dos nós |
| Scheduler | `control-plane/scheduler` | Escolher em qual nó cada canal roda |
| Painel | `web-panel` | Interface de operação |
| PostgreSQL | — | Configuração, atribuições, eventos, índice do DVR, usuários |
| Redis | — | Estado operacional de acesso rápido (status atual, fonte ativa) |
| NATS | — | Comandos e eventos entre Control Plane e nós |

O controller funciona por **reconciliação**: compara o estado desejado (gravado no banco) com o estado informado pelos nós e envia os comandos necessários para aproximar os dois. Assim, a recuperação depois de uma falha usa o mesmo mecanismo da operação normal.

## 4. Comunicação entre componentes

| Origem | Destino | Meio | Uso |
| --- | --- | --- | --- |
| Painel Web | API | HTTPS/REST (JSON) e WebSocket/SSE | Operação e status em tempo real |
| Sistemas externos | API | HTTPS/REST com token | Integrações |
| Controller | Agente | NATS (request/reply) | `start`, `stop`, `restart`, `switch-source` |
| Agente | Controller | NATS | Heartbeat, status e eventos |
| Agente | Prometheus | HTTP `/metrics` | Métricas |
| Agente | Loki | Envio de logs | Logs estruturados |
| API / Controller | PostgreSQL | SQL | Configuração e histórico |
| API / Controller | Redis | Protocolo Redis | Estado operacional |
| Clientes | Origem HTTP (nginx) | HTTP/HTTPS | HLS e MPEG-TS |

### 4.1 Subjects NATS [Proposta]

| Subject | Direção | Conteúdo |
| --- | --- | --- |
| `nexostream.worker.<worker_id>.heartbeat` | nó → controller | Recursos livres, canais em execução, versão |
| `nexostream.worker.<worker_id>.cmd` | controller → nó | Comandos para os canais |
| `nexostream.channel.<channel_id>.status` | nó → controller | Estado, fonte ativa, bitrate |
| `nexostream.channel.<channel_id>.event` | nó → controller | Failover, reinício, perda de sinal, recuperação |

Eventos de failover, falha e auditoria usam **JetStream** com persistência, para não se perderem quando o controller estiver reiniciando.

## 5. Ciclo de vida de um canal

```text
 1. Operador cadastra o canal (painel ou API)
 2. API grava a configuração no PostgreSQL (estado desejado = em execução)
 3. Scheduler escolhe um Media Node
 4. Controller envia "start" ao agente do nó via NATS
 5. Agente inicia o pipeline:
       ingestão → análise → remux ou transcodificação → empacotamento
                                                      ↘ DVR (se ativo)
 6. Agente publica status, eventos e métricas
 7. Controller compara o estado real com o desejado e corrige, se preciso
```

## 6. Estados do canal e failover

### 6.1 Estados [Proposta]

```text
 STOPPED ──► STARTING ──► RUNNING ◄──────────────┐
                │            │                    │
                ▼            ▼                    │
             FAILED ◄──  DEGRADED ──► SWITCHING ──► ON_BACKUP ──► RECOVERING
                ▲         (confirmação)    │                          │
                └──────────────────────────┘      fonte principal ok ─┘
```

| Estado | Significado |
| --- | --- |
| `STOPPED` | Parado pelo operador |
| `STARTING` | Pipeline sendo iniciado |
| `RUNNING` | Fonte principal ativa e saudável |
| `DEGRADED` | Problema detectado, dentro da janela de confirmação |
| `SWITCHING` | Trocando para a próxima fonte disponível |
| `ON_BACKUP` | Operando com uma fonte alternativa |
| `RECOVERING` | Verificando se a fonte principal voltou de forma estável |
| `FAILED` | Nenhuma fonte disponível ou limite de reinícios atingido |

### 6.2 Fluxo de failover

1. Monitorar continuamente a fonte ativa.
2. Detectar perda ou degradação conforme os limites configurados.
3. Confirmar a falha, para não trocar de fonte por oscilações breves.
4. Ativar a próxima fonte disponível por ordem de prioridade.
5. Registrar o evento, a causa e a duração.
6. Tentar voltar para a fonte principal conforme a política do canal.

### 6.3 Parâmetros iniciais [Proposta]

| Parâmetro | Valor inicial |
| --- | --- |
| Sem pacotes por | 3 s → `DEGRADED` |
| Janela de confirmação | 5 s |
| Bitrate abaixo de 30% da média móvel por | 10 s → `DEGRADED` |
| Ausência de vídeo ou áudio por | 5 s → `DEGRADED` |
| Fonte principal estável antes de voltar | 60 s |
| Espera entre reinícios | 1 s, 2 s, 4 s… até 30 s |
| Limite de reinícios | 5 em 10 min → `FAILED` e alerta |

## 7. Cluster e scheduler

### 7.1 Responsabilidades do controller

- Registrar os workers e seus recursos.
- Verificar disponibilidade e saúde.
- Alocar canais considerando CPU, memória, GPU e rede.
- Evitar sobrecarga de um único nó.
- Reprogramar canais quando um nó falhar.
- Expor o estado do cluster no painel.

### 7.2 Funcionamento [Proposta]

- **Heartbeat** a cada 5 s. O nó é considerado indisponível após 3 heartbeats perdidos (15 s) e seus canais são realocados.
- **Custo estimado do canal:** passthrough tem custo baixo; transcodificação tem custo proporcional à resolução, ao FPS e ao número de perfis. Os custos reais vêm dos benchmarks.
- **Pontuação do nó:** recursos livres menos o custo do canal. O canal vai para o nó com melhor pontuação que continue abaixo dos limites (por exemplo, 80% de CPU ou GPU).
- **Afinidade:** canais com fonte multicast precisam ir para nós conectados à rede de ingestão correspondente. Canais que usam GPU só vão para nós com GPU.
- **Drain:** um nó em manutenção não recebe canais novos e os atuais são migrados aos poucos.
- **Persistência:** a atribuição canal → nó fica no PostgreSQL, para o controller reconstruir o estado depois de reiniciar.

### 7.3 Separação de funções no futuro

Quando a carga exigir, os nós poderão ser especializados em ingestão, transcodificação, distribuição/edge ou armazenamento DVR. A topologia inicial de laboratório é de 4 nós com 250 canais cada, uma hipótese de teste e não uma capacidade garantida.

## 8. DVR, Timeshift e Catch-up

### 8.1 Gravação

O módulo de DVR grava os mesmos segmentos produzidos para HLS e registra seus metadados (canal, horário de início, duração, caminho e tamanho). Em canal com ABR, por padrão só a qualidade mais alta é gravada. Com `all_qualities` ligado, todas são: a mais alta na pasta do canal e as outras em `v<N>/`, e o espaço em disco cresce com a soma delas.

Organização em disco [Proposta]:

```text
/srv/nexostream/dvr/<canal>/<AAAA>/<MM>/<DD>/<HH>/<timestamp_unix>.ts
```

### 8.2 Reprodução

- **Timeshift:** a playlist ao vivo mantém uma janela maior de segmentos, permitindo pausar e voltar.
- **Catch-up:** a API gera uma playlist HLS do tipo VOD para o intervalo `from`–`to` a partir do índice.
- As playlists usam `#EXT-X-PROGRAM-DATE-TIME` para que o player saiba o horário real de cada segmento.

### 8.3 Retenção

- Retenção por canal ou por grupo.
- A limpeza remove diretórios de hora inteiros já fora da retenção e atualiza o índice.
- Alertas de espaço em disco e verificação de integridade dos arquivos.

## 9. Resiliência do próprio Control Plane

| Componente | Falha | Efeito esperado |
| --- | --- | --- |
| API | Indisponível | Painel e integrações param; canais continuam no ar |
| Controller | Indisponível | Sem realocação automática; canais continuam no ar; failover local continua |
| NATS | Indisponível | Comandos e eventos param; agentes mantêm a última configuração e guardam eventos localmente até reconectar [Proposta] |
| PostgreSQL | Indisponível | Sem alterações de configuração; estado atual mantido em Redis e nos agentes |
| Media Node | Indisponível | Canais realocados para outros nós após 15 s [Proposta] |

O failover de fonte roda **dentro do agente**, sem depender do Control Plane. Assim, ele continua funcionando mesmo com o controle central fora do ar.

Redundância do próprio Control Plane (réplicas da API, PostgreSQL com réplica, cluster NATS) é uma decisão pendente para a primeira versão de produção.

## 10. Evolução prevista

1. Um nó, um canal UDP/MPEG-TS, saída HLS e TS.
2. Transcodificação H.264/AAC com perfis.
3. API, painel e DVR.
4. Failover de fonte e supervisão.
5. Vários nós com scheduler.
6. Novos protocolos de entrada (SRT, RTMP, RTSP, HTTP).
7. DASH, ABR com vários perfis e separação de funções entre nós.

As decisões tomadas ao longo dessa evolução ficam registradas em [adr/](adr/README.md).

## 11. Implementação atual (MVP)

Esta seção descreve o que existe no código em 16/11/2026: MVP de um nó, com cluster funcional, mas ainda não validado em laboratório. As escolhas marcadas como ADR "Proposta" aguardam aprovação da equipe.

### 11.1 Caminho dos dados em um Media Node

```text
 fonte UDP/RTP ──► receptor (Go) ──► analisador MPEG-TS ──► medidas ──► máquina de estados
 (principal)        socket por fonte   PAT/PMT, CC, PCR,        │            (health.Machine)
                                       bitrate, áudio/vídeo     │                 │ Desired
 fonte UDP/RTP ──► receptor (Go) ──┐                            ▼                 ▼
 (backup, só quando                │                    status, eventos,   runner do canal
  necessário)                      │                    métricas           │
                                   └── pacotes da fonte ativa ──► fila ──► stdin do ffmpeg
                                                                              │ tee
                                         ┌────────────────────────────────────┼──────────────┐
                                         ▼                                    ▼              ▼
                           HLS em /var/lib/nexostream/hls/<id>/   UDP loopback → relay   MPEG-TS/UDP
                           (servido pelo worker, nginx na frente)  HTTP /live/<id>/stream.ts (opcional)
```

- A recepção e a análise ficam no agente ([ADR 0004](adr/0004-ingestao-e-analise-no-agente.md)); decodificação e codificação ficam no FFmpeg ([ADR 0003](adr/0003-ffmpeg-como-engine-inicial.md)).
- A fonte principal é recebida também durante `ON_BACKUP`, para detectar o retorno. As demais fontes só são abertas em `SWITCHING` e em `FAILED`.
- A troca de fonte reinicia o processo da engine. A playlist continua (`append_list`) com `#EXT-X-DISCONTINUITY`.
- Mudar nome ou grupo do canal não reinicia o pipeline. Mudar fontes, modo, perfis ou saídas reinicia.

### 11.2 Módulos

| Pasta | Conteúdo |
| --- | --- |
| `internal/model`, `internal/protocol` | Tipos de domínio e mensagens NATS |
| `internal/bus` | NATS/JetStream e implementação em memória |
| `media-engine/ingest` | Receptor UDP/RTP, multicast, analisador MPEG-TS, gerador sintético para testes |
| `media-engine/health` | Bitrate, janelas e a máquina de estados do canal |
| `media-engine/pipeline` | Decisão passthrough/transcodificação, interface `Engine`, FFmpeg, ffprobe, detecção de encoders |
| `media-engine/transcoding` | Argumentos de encoder (libx264, NVENC, QSV) e validação de perfis |
| `media-engine/packaging` | HLS (tee), relay MPEG-TS/HTTP, leitura de playlist |
| `media-engine/dvr` | Gravação em disco (uma hora por arquivo, com índice), catch-up, timeshift, exportação em MP4, retenção e integridade |
| `data-plane/agent` | Agente: runners, comandos, heartbeat, métricas, estado local |
| `control-plane/controller` | Reconciliação, workers offline, realocação, drain, persistência de eventos |
| `control-plane/scheduler` | Escolha do nó (restrições e pontuação) |
| `control-plane/store` | Contrato, PostgreSQL com migrações e memória |
| `control-plane/statuscache` | Status atual em Redis ou memória |
| `control-plane/sharedstate` | Estado que as instâncias da API dividem (limite de login, idempotência), em Redis ou memória |
| `control-plane/boot` | Abre PostgreSQL e Redis para os binários do Control Plane (o worker não os carrega) |
| `control-plane/api` | REST `/api/v1`, autenticação, RBAC, auditoria, SSE, OpenAPI |
| `cmd/*` | Binários `nexostream-api`, `-controller`, `-worker` e `-dev` |

### 11.2.1 Fronteiras de dependência

```text
 cmd/nexostream-worker ──► data-plane/agent ──► media-engine/* ──► internal/*
 cmd/nexostream-api ─┐
 cmd/nexostream-controller ──► control-plane/* ──► media-engine/{drm,dvr,...}* ──► internal/*
                     └ (a API e o controller falam com o worker só pelo NATS)
```

Regras, conferidas por `tests/architecture` ([ADR 0012](adr/0012-fronteiras-do-codigo-e-estado-compartilhado.md)):

- `data-plane/` e `media-engine/` não importam `control-plane/`, e `media-engine/` também não importa `data-plane/`;
- `control-plane/` não importa `data-plane/`;
- `internal/` fica abaixo dos dois planos;
- o binário do worker não carrega código de banco nem de Redis.

A engine de mídia é um contrato executável: `pipelinetest.RunConformance` define o que o agente espera de qualquer `pipeline.Engine`, e a engine FFmpeg o cumpre. Uma segunda engine (por exemplo, GStreamer) está pronta para ser plugada quando passar nessa suíte.

### 11.3 Comportamentos definidos na implementação

Estes pontos não estavam fechados na especificação e foram definidos no código. Revise antes de aprovar os ADRs:

| Tema | Comportamento atual |
| --- | --- |
| Troca manual para um backup | Suspende o retorno automático à principal até a próxima troca manual, reinício ou partida (evita desfazer a decisão do operador) |
| `FAILED` por falta de fonte | Todas as fontes ficam em escuta; o canal reinicia sozinho quando alguma volta |
| `FAILED` por limite de reinícios | Nova tentativa automática após 10 min, ou antes com reinício manual |
| Reinícios do pipeline | Espera de 1 s, 2 s, 4 s… até 30 s; zera depois de 60 s rodando; o 6º encerramento em 10 min leva a `FAILED` |
| Pipeline travado | Recebendo entrada, mas sem progresso por 20 s: o processo é encerrado e reiniciado |
| `start`/`stop` pela API | Alteram o estado desejado (`enabled`); o controller aplica. `restart` e `switch-source` vão direto ao worker |
| HEVC | Em modo `auto`, HEVC é transcodificado, a menos que a opção esteja ligada em Configurações › Geral ou em `NEXOSTREAM_ALLOW_HEVC_PASSTHROUGH=true` |
| Espectadores | Contados apenas no relay MPEG-TS/HTTP; HLS deve ser contado na origem (logs do nginx) |
| Sessões e autorização ([ADR 0009](adr/0009-sessoes-autorizacao-e-epg.md)) | O worker serve todo o HLS, conta as sessões (token ou IP + player; 30 s sem pedidos fecha) e aplica as regras do canal: IPs bloqueados e liberados em todo pedido, token do cluster, backend HTTP com cache, limite de sessões do canal no servidor que o roda e limite por assinante (`X-Max-Sessions`) somado entre os servidores de forma aproximada, pelo NATS (intervalo de 5 s; um servidor mudo por 15 s deixa de contar) |
| Áudio | Todas as faixas de áudio da entrada são mantidas; se alguma não for AAC, todas são convertidas |
| ABR ([ADR 0011](adr/0011-abr-com-um-decode.md)) | Uma decodificação, uma escala e um encode por qualidade; `v<N>/` por qualidade e playlist mestre do worker; MPEG-TS e miniatura usam a mais alta; o DVR grava só a mais alta, ou todas com `dvr.all_qualities` (`v<N>/` em disco, playlist mestre no catch-up) |
| Encoder de hardware | Se o QSV ou o NVENC derrubam o pipeline, ficam 30 min em quarentena no servidor; perfis `auto` voltam em CPU (evento `worker.encoder_failed`) |
| Controller ([ADR 0010](adr/0010-controller-redundante-com-lease.md)) | Várias instâncias, com reconciliação só no dono da lease `controller` (TTL de 15 s, relógio do banco) |
| Estatísticas | Uma amostra por minuto gravada pelo líder e guardada por 30 dias; a página Estatísticas lê `/stats` |
| Alertas | Avaliados na API a partir do estado atual (`/alerts`); as mesmas regras existem no Prometheus para notificação |
| Integridade do DVR | A cada 5 min, as horas fechadas do último dia são conferidas (índice contínuo, tamanho, sincronismo MPEG-TS) |
| Push | Um FFmpeg por destino, alimentado pelo relay MPEG-TS do canal; reconecta sozinho (1 s a 30 s); o status mostra cada destino |
| DASH | Saída do mesmo `tee` (fMP4, template com timeline); os streams copiados ganham as tags `avc1`/`hvc1` e `mp4a`, porque o MP4 rejeita as do MPEG-TS |
| Legendas | DVB e teletexto copiados só para as saídas MPEG-TS |
| Histórico de sessões | Lotes de sessões encerradas pelo NATS (best effort), gravados de forma idempotente por todos os controllers; 30 dias |
| Papéis dos servidores | `passthrough`, `transcode` e `dvr`, definidos no painel; sem papéis, o servidor aceita tudo |
| EPG | Importado pelo controller a cada 6 h (uma vez por URL); guarda de 7 dias atrás a 14 dias à frente; programas que acabaram há mais de 30 dias são apagados |
| DRM ([ADR 0008](adr/0008-drm-aes-128-com-token.md)) | HLS AES-128 cifrado na entrega pelo worker; HLS em claro em `<HLS_DIR>/.drm/<canal>` (não publicado); chave derivada por canal e período, entregue só com token; DVR em disco continua em claro e é cifrado ao servir; sem segredos o nó responde `503` |
| Custos do scheduler | Valores provisórios, ajustáveis em Configurações › Cluster; precisam ser calibrados com benchmark |
| Configurações do sistema | Editadas no painel e enviadas aos nós pelo comando `configure`; o nó guarda uma cópia local e só reinicia os pipelines se mudar algo de mídia ([ADR 0007](adr/0007-configuracoes-gerenciadas-pelo-painel.md)) |
| Eventos no agente sem NATS | Fila em memória (até 10.000 eventos, descarta os mais antigos), com reenvio automático |
