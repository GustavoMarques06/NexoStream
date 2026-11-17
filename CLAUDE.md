# CLAUDE.md

Contexto para o Claude Code trabalhar neste repositório.

## O que é o projeto

NexoStream é um Media Server IPTV próprio para Linux bare metal: ingestão, análise, passthrough/transcodificação, empacotamento HLS/MPEG-TS, DVR/Timeshift, failover e gerenciamento de um cluster de servidores de mídia. A meta de dimensionamento é de 1.000 canais simultâneos, mas ainda **não está comprovada**.

O documento-base é a especificação em [../NexoStream_Projeto.md](../NexoStream_Projeto.md). Quando houver conflito entre ela e outro arquivo, a especificação prevalece.

## Status atual

- Fases 1 a 7 implementadas em código (inclui ABR, ADR 0011; gravação do DVR; controller redundante com lease, ADR 0010). Nada foi validado em laboratório ainda. Pendências em [PENDENCIAS.md](PENDENCIAS.md).
- Decisões tomadas com o usuário para o MVP: FFmpeg atrás da interface `pipeline.Engine` (ADR 0003), ingestão e análise MPEG-TS no agente (ADR 0004), infraestrutura real com adaptadores em memória para o modo dev (ADR 0005). Os três ADRs estão como "Proposta".
- Comportamentos definidos no código e que precisam de revisão estão em `docs/architecture.md`, seção 11.3.
- Tudo o que estiver marcado como **[Proposta]** na documentação é sugestão, não decisão. Não trate proposta como requisito sem confirmação do usuário.

## Diretrizes obrigatórias

1. **Não criar motor de codecs do zero.** Integrar GStreamer e FFmpeg. O desenvolvimento próprio fica na orquestração, no gerenciamento, na automação, na observabilidade e na experiência operacional.
2. **Passthrough/remux primeiro.** Só transcodificar quando o formato de entrada não servir para a saída ou quando o operador pedir.
3. **Data Plane isolado do Control Plane.** Os pipelines de mídia precisam continuar funcionando se a API ou o controller cair.
4. **Falhar de forma controlada.** Toda falha de pipeline deve gerar um estado, um evento e uma métrica, com reinício limitado e espera exponencial.
5. **Nada de números inventados.** Capacidade, consumo e latência só podem ser afirmados com benchmark registrado.
6. **Segurança.** Segredos nunca entram no código nem no repositório. Interfaces expostas usam TLS. Ações administrativas geram auditoria.
7. **Escopo da v1:** UDP/MPEG-TS na entrada, H.264/AAC na transcodificação, HLS e MPEG-TS na saída. Outros protocolos entram de forma incremental.

## Stack

| Camada | Tecnologia |
| --- | --- |
| Mídia | GStreamer, FFmpeg (C++/Rust somente se os testes exigirem) |
| API, controller, scheduler, agente | Go |
| Painel | React + TypeScript |
| Dados | PostgreSQL (configuração e histórico), Redis (estado operacional) |
| Mensageria | NATS, JetStream para eventos que não podem se perder |
| Observabilidade | Prometheus, Grafana, Loki |
| SO | Ubuntu Server LTS |

## Estrutura

```text
cmd/{nexostream-api,nexostream-controller,nexostream-worker,nexostream-dev}
control-plane/{api,controller,scheduler,store/{memory,postgres,storetest},statuscache}
data-plane/agent                  # agente do Media Node; NÃO pode importar control-plane/ (há teste: tests/architecture)
media-engine/{ingest,health,pipeline,transcoding,packaging,dvr,drm,sessions}
internal/{model,protocol,bus,config,logging,runtime,version}
web-panel/{src,public}            # React + TS (Vite)
deploy/{systemd,configs,scripts}  # + docker-compose.dev.yml, dev.env
tests/{integration,recovery,load}
docs/{architecture,api,deployment,operations}.md + docs/adr/
```

Pontos de entrada úteis:

- Máquina de estados do canal: `media-engine/health/machine.go` (pura, testada com relógio simulado).
- Supervisão por canal no worker: `data-plane/agent/runner.go`.
- Argumentos do FFmpeg: `media-engine/pipeline/ffmpeg.go` (`BuildArgs`).
- Reconciliação do cluster: `control-plane/controller/controller.go`.
- Configurações do sistema (página Configurações, blocos `settings`/`server` do editor de texto): `control-plane/settings/`, `control-plane/api/settings.go`, `data-plane/agent/settings.go` (ADR 0007).
- Abas do canal no estilo Flussonic (Input, Transcoder, DVR, Output, EPG, Auth, Play sessions): `web-panel/src/pages/channel/tabs.tsx`. Sessões e autorização: `media-engine/sessions/`, `data-plane/agent/sessions.go`. EPG (XMLTV): `control-plane/epg/` (ADR 0009).
- ABR: `media-engine/pipeline/ffmpeg.go` (split + encode por qualidade), `media-engine/packaging/abr.go` (mestre), `data-plane/agent/abr.go` (ADR 0011). Redundância do controller: `control-plane/controller/controller.go` (`lead`, ADR 0010). Alertas: `control-plane/alerts/`. Estatísticas: `control-plane/controller/stats.go`, `control-plane/api/stats.go`, `web-panel/src/pages/Stats.tsx`.
- DRM (HLS AES-128 com token, ADR 0008): `media-engine/drm/` (chaves, token, playlist), `data-plane/agent/drm.go` (rotas `/hls/`), `media-engine/dvr/handler.go` (DVR cifrado), `control-plane/api/playback.go` (token).
- Rotas da API: `control-plane/api/server.go`; OpenAPI em `control-plane/api/openapi.json` (manter em sincronia).
- Schema: `control-plane/store/postgres/migrations/` (formato golang-migrate; nunca editar uma migração já aplicada, crie outra).
- Mudança no contrato `store.Store` ou `bus.Bus`: atualizar as duas implementações e a suíte de conformidade. O `store.Store` é a composição de interfaces por assunto (`store.UserStore`, `store.SettingsStore` e outras): um componente novo deve depender só da interface de que precisa.
- Fronteiras (ADR 0012): `data-plane/` e `media-engine/` não importam `control-plane/`; o binário do worker não carrega código de banco; `tests/architecture` confere. O estado que várias instâncias da API precisam dividir (limite de login, idempotência) vai em `control-plane/sharedstate`, não em memória. Mudança incompatível no contrato controller-worker: subir `protocol.Version`.

## Convenções

- **Whitelabel:** o repositório não carrega a marca de nenhuma empresa (nem logo, nem nome, nem favicon). O logo padrão é um símbolo genérico em SVG (`DefaultMark` em `web-panel/src/branding.tsx`, que segue a cor de destaque), e cada instalação define o nome, o logo, a cor e a mensagem em Configurações › Geral. Não adicione imagens de marca ao painel nem nomes de empresa no código ou nos textos.

- **Idioma:** documentação em português; código, identificadores, nomes de métricas, campos de API e chaves de configuração em inglês.
- **Commits:** Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).
- **Go:** `gofmt`, `golangci-lint`, `go test ./...`. Erros com contexto (`fmt.Errorf("...: %w", err)`). Logs estruturados em JSON com `channel_id` e `worker_id` quando existirem.
- **Frontend:** TypeScript `strict`, ESLint e Prettier.
- **Banco:** migrações versionadas; nunca alterar o schema manualmente.
- **API:** REST em `/api/v1`, documentada em OpenAPI 3. Ver [docs/api.md](docs/api.md).
- **Métricas:** prefixo `nexostream_`, unidades base (bytes, segundos), contadores com sufixo `_total`.
- **Decisões de arquitetura:** registrar um ADR em `docs/adr/` (modelo em [docs/adr/README.md](docs/adr/README.md)).

## Onde encontrar cada assunto

| Assunto | Arquivo |
| --- | --- |
| Componentes, fluxos, estados do canal, scheduler, DVR | [docs/architecture.md](docs/architecture.md) |
| Endpoints, autenticação, erros, eventos | [docs/api.md](docs/api.md) |
| Hardware, rede, sysctl, systemd, nginx, portas | [docs/deployment.md](docs/deployment.md) |
| Procedimentos, alertas, diagnóstico, testes | [docs/operations.md](docs/operations.md) |
| Segredos, rotação de chaves, incidentes, auditoria | [docs/seguranca.md](docs/seguranca.md) |
| Roteiro de laboratório (fases 2 a 8) | [docs/laboratorio.md](docs/laboratorio.md) |
| Requisitos, dimensionamento, riscos, plano | [../NexoStream_Projeto.md](../NexoStream_Projeto.md) |

## Decisões ainda em aberto

Antes de implementar algo que dependa destes pontos, pergunte ao usuário:

- Os 1.000 canais serão apenas recebidos ou também transcodificados?
- Bitrate médio e máximo por canal e número de espectadores simultâneos.
- Protocolos de entrada e formatos de saída obrigatórios na v1.
- Retenção do DVR e quais canais serão gravados.
- Redundância de fontes para todos os canais.
- Hardware disponível (CPU, GPU, RAM, rede, armazenamento).
- Distribuição pelos próprios Media Nodes ou por edge/CDN.
- Núcleo de mídia em GStreamer, FFmpeg ou combinação dos dois.

## Comandos

| Tarefa | Comando |
| --- | --- |
| Todos os testes Go | `go test ./...` (cerca de 1 min; `-short` pula integração e NATS embarcado) |
| Store PostgreSQL | `NEXOSTREAM_TEST_DATABASE_URL=postgres://… go test ./control-plane/store/postgres/`, ou `NEXOSTREAM_TEST_EMBEDDED_PG=1` para baixar e subir um Postgres embarcado (pulado sem nenhuma das duas) |
| E2E com FFmpeg real | `go test ./tests/integration/ -run FFmpeg` (pulado se não houver `ffmpeg` no PATH) |
| Formatação e vet | `gofmt -l .` e `go vet ./...`; `golangci-lint run` no CI |
| Painel | `cd web-panel && npm install && npm run lint && npm run typecheck && npm run build` |
| Rodar tudo localmente | `go run ./cmd/nexostream-dev [-demo]` (em memória; painel em `web-panel/dist`) |
| Binários | `go build -o bin/ ./cmd/...` ou `make build-linux` |

O race detector (`go test -race`) precisa de cgo; roda no CI Linux.
