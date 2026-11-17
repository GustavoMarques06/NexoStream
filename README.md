# NexoStream

**Media Server IPTV modular, observável e escalável.**

O NexoStream é uma plataforma própria para receber, processar, monitorar e distribuir canais de vídeo ao vivo em servidores Linux bare metal. A arquitetura separa o processamento de mídia (*Data Plane*) do gerenciamento (*Control Plane*) e foi pensada para crescer horizontalmente, com vários servidores de mídia coordenados por um controle central.

> **Status:** funcionalidades das fases 1 a 7 implementadas e cobertas por testes automatizados (inclusive com FFmpeg e PostgreSQL reais), mas **ainda não validadas em laboratório** com fontes, players e hardware reais. O que falta, e as decisões pendentes, estão em [PENDENCIAS.md](PENDENCIAS.md). A meta de **1.000 canais simultâneos** é um objetivo de dimensionamento, não uma capacidade comprovada; ela será validada pelos testes progressivos de [`tests/load`](tests/load/README.md).

---

## O que já funciona

| Área | Situação |
| --- | --- |
| Ingestão | UDP, RTP (unicast e multicast por interface), SRT (inclusive como listener, com senha), RTMP(S) (inclusive recebendo de um encoder, com chave validada), RTSP e HTTP(S), com failover entre fontes e análise MPEG-TS no agente: PAT/PMT, codecs, erros de continuidade, PCR, bitrate e média móvel |
| Processamento | Decisão automática entre passthrough, conversão só do áudio e transcodificação; modo forçado por canal |
| Transcodificação | H.264/AAC com libx264, NVENC ou Quick Sync (com volta para CPU se o encoder de hardware falhar), GOP fixo, desentrelaçamento automático, ABR de 2 a 6 qualidades com uma decodificação ([ADR 0011](docs/adr/0011-abr-com-um-decode.md)) |
| Saída | HLS, MPEG-DASH, MPEG-TS sobre HTTP (relay do worker), MPEG-TS sobre UDP e push para RTMP, RTMPS e SRT. Legendas DVB e teletexto são copiadas para as saídas MPEG-TS |
| DVR e timeshift | Gravação local no servidor (uma pasta ou vários discos, em modo RAID-0 ou RAID-1), uma hora por arquivo com índice, catch-up e timeshift por URL no estilo Flussonic, retenção por canal, verificação de integridade e, em canal ABR, a opção de gravar todas as qualidades |
| DRM e autorização | HLS AES-128 com token ([ADR 0008](docs/adr/0008-drm-aes-128-com-token.md)); regras por canal: IP liberado ou bloqueado, token e backend HTTP ([ADR 0009](docs/adr/0009-sessoes-autorizacao-e-epg.md)) |
| Sessões e EPG | Sessões ao vivo e histórico de 30 dias; importação de guia XMLTV |
| Failover | Máquina de estados da especificação (8.4/8.5): troca para backup, retorno à principal após estabilidade, reinício com espera exponencial e limite |
| Data Plane independente | O worker guarda a última configuração e mantém os canais no ar sem o Control Plane |
| Cluster | Controller redundante com lease ([ADR 0010](docs/adr/0010-controller-redundante-com-lease.md)), heartbeats, nó `OFFLINE` após 15 s, realocação automática, scheduler com restrições (interface, encoder, CPU, papéis) e drain sem interrupção |
| API | REST `/api/v1` com OpenAPI 3, JWT com logout, revogação de sessão, renovação rotativa com detecção de reuso e bloqueio de conta, tokens de integração com escopo, RBAC, auditoria append-only, limite de login por IP e por usuário, paginação por cursor, idempotência e SSE |
| Painel | React + TypeScript: dashboard, canais (abas no estilo Flussonic: entrada, transcodificador, DVR, saída, EPG, autorização e sessões), sessões, servidores, perfis, alertas, estatísticas, eventos, auditoria, usuários, tokens, configurações e marca editáveis (whitelabel), editor de configuração em texto e documentação em `/docs` |
| Observabilidade | Métricas Prometheus da seção 14.1, logs JSON, regras de alerta, dashboard Grafana, Promtail→Loki |
| Implantação | systemd, nginx, sysctl, NATS/JetStream, `install.sh` e Docker Compose para desenvolvimento |

## O que ainda falta

Resumo; a lista completa, com as decisões que dependem do responsável pelo projeto, está em [PENDENCIAS.md](PENDENCIAS.md).

- **Validação em laboratório** (Fases 2, 3, 5, 6, 7 e 8): ingestão por 24 h, consumo por perfil em CPU e GPU, DVR por 72 h, failover com quedas reais, cluster com vários servidores e escala de 10 a 1.000 canais. Os argumentos de NVENC e Quick Sync ainda não rodaram em hardware real, e não há matriz de testes com players reais.
- **Recursos do Flussonic que faltam:** faixas de áudio alternativas no HLS, LL-HLS, DRM comercial (Widevine, FairPlay e PlayReady), WebRTC, publish por RTMP, overlay de logo, exportação do DVR em MP4 e marcadores SCTE-35.
- **Pacote Linux:** hoje são três unidades systemd e um `install.sh`; falta o pacote `.deb` único, com TLS configurado na instalação.
- **Decisões pendentes** da especificação (seção 21) e dos ADRs de [0003](docs/adr/0003-ffmpeg-como-engine-inicial.md) a [0013](docs/adr/0013-autenticacao-reforcada.md), todos com status "Proposta".

## Início rápido (desenvolvimento, sem infraestrutura)

Requisitos: Go 1.26+ e Node 20+. O FFmpeg é necessário para processar mídia; sem ele, a API, o painel e o failover funcionam, mas o pipeline falha de forma controlada.

```bash
cd web-panel && npm install && npm run build && cd ..
go run ./cmd/nexostream-dev -demo
```

O terminal mostra o endereço do painel com o IP da máquina (por exemplo `http://192.168.0.10:8080`) e a senha do `admin`, gerada a cada execução. O servidor escuta em todas as interfaces, então outros computadores da rede também abrem o painel. Para restringir à própria máquina, use `-addr 127.0.0.1:8080 -media-addr 127.0.0.1:9500`; para fixar o endereço anunciado, `-host`; para fixar a senha, `-admin-password`. Sem TLS no modo dev. Com `-demo`, o FFmpeg gera duas fontes de teste (principal e backup) e cria o canal `demo`. Para ver o failover, encerre o processo `ffmpeg` da fonte principal.

O modo `nexostream-dev` junta API, controller e um worker em um processo, com banco, cache e mensageria em memória: tudo se perde ao encerrar. Ele não serve para produção ([ADR 0005](docs/adr/0005-contratos-de-infraestrutura-e-modo-dev.md)).

## Stack completa (PostgreSQL, Redis, NATS)

```bash
docker compose -f deploy/docker-compose.dev.yml up -d     # Postgres, Redis, NATS, Prometheus, Loki, Grafana
set -a && . deploy/dev.env && set +a
go run ./cmd/nexostream-api &
go run ./cmd/nexostream-controller &
go run ./cmd/nexostream-worker
```

Grafana em `http://127.0.0.1:3000`, com o dashboard "NexoStream — Visão geral".

## Produção (Ubuntu Server LTS)

```bash
make build-linux panel
sudo ./deploy/scripts/install.sh worker    # em cada Media Node
sudo ./deploy/scripts/install.sh control   # no nó de controle
```

Edite `/etc/nexostream/*.env` (modelos em [`deploy/configs`](deploy/configs)) e siga [docs/deployment.md](docs/deployment.md). Segredos vão em arquivos `*_FILE` com permissão restrita, nunca no repositório.

## Arquitetura resumida

```text
 Painel Web (React/TS) ──► nexostream-api (Go) ──► PostgreSQL · Redis · NATS/JetStream
                                                        │
                                         nexostream-controller (reconciliação, scheduler)
                                                        │ comandos / heartbeats / eventos
               ┌────────────────────────────────────────┼───────────────────────┐
               ▼                                        ▼                       ▼
     nexostream-worker (Media Node 1)          Media Node 2             ...  Media Node N
     UDP → análise MPEG-TS → failover → ffmpeg (remux/transcode) → HLS · TS/HTTP · TS/UDP
               │
               ▼
        nginx (origem) ──► aplicações clientes
```

Detalhes em [docs/architecture.md](docs/architecture.md), incluindo a [implementação atual](docs/architecture.md#11-implementação-atual-mvp).

## Estrutura do repositório

```text
nexostream/
├── cmd/                 # nexostream-api, -controller, -worker e -dev (all-in-one)
├── control-plane/
│   ├── api/             # REST /api/v1, auth, RBAC, auditoria, SSE, OpenAPI
│   ├── controller/      # Reconciliação, realocação, drain, eventos
│   ├── scheduler/       # Escolha do nó
│   ├── store/           # PostgreSQL (migrações) e memória
│   └── statuscache/     # Redis e memória
├── data-plane/
│   └── agent/           # Agente do Media Node (não depende do control-plane)
├── media-engine/
│   ├── ingest/          # UDP/RTP, SRT/RTMP/RTSP/HTTP (via FFmpeg), analisador MPEG-TS
│   ├── health/          # Bitrate e máquina de estados do canal
│   ├── pipeline/        # Decisão, interface Engine, FFmpeg
│   ├── transcoding/     # libx264, NVENC, QSV
│   ├── packaging/       # HLS, DASH, ABR, relay MPEG-TS/HTTP
│   ├── dvr/             # Gravação, índice, catch-up, retenção e integridade
│   ├── drm/             # HLS AES-128 com token
│   └── sessions/        # Sessões e autorização (IP, token, backend HTTP)
├── internal/            # Tipos, protocolo NATS, bus, config, logs
├── web-panel/           # Painel React + TypeScript (Vite)
├── deploy/              # systemd, nginx, sysctl, NATS, Prometheus, Grafana, scripts
├── tests/               # integration (pilha completa), recovery (failover), load (carga)
└── docs/                # Arquitetura, API, implantação, operação, ADRs
```

## Comandos

| Tarefa | Comando |
| --- | --- |
| Testes Go | `go test ./...` (sem os de integração: `go test -short ./...`) |
| Testes do PostgreSQL | `NEXOSTREAM_TEST_DATABASE_URL=postgres://… go test ./control-plane/store/postgres/`, ou `NEXOSTREAM_TEST_EMBEDDED_PG=1` para baixar e subir um PostgreSQL embarcado |
| Lint Go | `gofmt -l .`, `go vet ./...`, `golangci-lint run` |
| Painel | `cd web-panel && npm run dev` (proxy para a API em :8080), `npm run lint`, `npm run build` |
| Binários | `make build` ou `make build-linux` |
| Migrações | `nexostream-api -migrate` (aplica e sai; também roda na inicialização com `NEXOSTREAM_DB_MIGRATE=true`) |

## Documentação

| Documento | Conteúdo |
| --- | --- |
| [Especificação do projeto](../NexoStream_Projeto.md) | Documento-base com requisitos, dimensionamento, riscos e plano |
| [docs/architecture.md](docs/architecture.md) | Componentes, fluxos, estados do canal, cluster, DVR e implementação atual |
| [docs/api.md](docs/api.md) | Convenções, endpoints, autenticação e eventos |
| [docs/deployment.md](docs/deployment.md) | Hardware, rede, sistema, serviços e instalação |
| [docs/operations.md](docs/operations.md) | Procedimentos, alertas, diagnóstico e testes |
| [docs/seguranca.md](docs/seguranca.md) | Segredos, rotação de chaves, resposta a incidentes e auditoria |
| [docs/adr/](docs/adr/README.md) | Decisões de arquitetura |
| [tests/load/](tests/load/README.md) | Roteiro e registro de benchmarks |

## Roadmap

| Fase | Entrega | Situação |
| --- | --- | --- |
| 1 | Fundação do repositório, ambiente e padrões | Concluída (CI, lint, testes, Makefile) |
| 2 | Ingestão UDP/MPEG-TS e saída básica | Implementada; falta o teste de 24 h no laboratório |
| 3 | Transcodificação e perfis de saída | Implementada, com ABR; falta medir o consumo por perfil |
| 4 | API e painel administrativo inicial | Implementada |
| 5 | DVR, Timeshift e retenção | Implementada (gravação, índice, catch-up, timeshift, retenção, integridade e ABR opcional); falta o teste de 72 h no laboratório |
| 6 | Monitoramento, recuperação e failover | Implementada; falta validar com fontes reais |
| 7 | Cluster e distribuição de canais | Implementada (scheduler, realocação, drain); falta validar com 2+ nós físicos |
| 8 | Testes de carga, estabilidade e ajustes | Ferramentas prontas; execução pendente |

## Princípios

- Estabilidade e observabilidade antes de novas funcionalidades.
- Não transcodificar sem necessidade.
- Integrar componentes de mídia maduros em vez de criar um motor de codecs.
- Projetar para falhar e se recuperar de forma controlada.
- Medir com streams reais e registrar cada benchmark.

## Uso responsável

A plataforma deve ser usada apenas para distribuir conteúdo sobre o qual a operação tenha direitos e autorização.

## Licença

A definir. Revise as licenças do FFmpeg, do x264 e dos codecs com patente (AC-3, HEVC) antes da produção.
