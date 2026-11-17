# NexoStream: pendências

Situação em 17/11/2026. Este documento resume o que ainda falta no projeto, para quem vai continuar o trabalho sem ter acompanhado o histórico.

## Contexto

O NexoStream é um Media Server IPTV whitelabel para Linux bare metal, com o Flussonic como referência de recursos e de modelo operacional.

- **Backend:** Go. Inclui a API, o controller, o scheduler e o agente de cada servidor de mídia.
- **Painel:** React + TypeScript.
- **Mídia:** FFmpeg, atrás da interface `pipeline.Engine`.
- **Dados e mensageria:** PostgreSQL, Redis e NATS.
- **Modo dev:** `go run ./cmd/nexostream-dev` sobe tudo em memória. O painel abre em `http://127.0.0.1:8080`.

O [CLAUDE.md](CLAUDE.md) traz a estrutura, as convenções e os comandos. A especificação que manda em caso de conflito é [../NexoStream_Projeto.md](../NexoStream_Projeto.md).

### Já implementado e testado (testes automatizados, sem laboratório)

- **Entrada:** UDP, RTP, SRT (inclusive em modo listener), RTMP(S), RTSP e HTTP(S), com failover entre fontes.
- **Processamento:** passthrough ou transcodificação, incluindo ABR com uma decodificação e QSV/NVENC com volta para CPU.
- **Saídas:** HLS, MPEG-DASH, MPEG-TS por HTTP, MPEG-TS por UDP e push para RTMP/RTMPS/SRT.
- **Legendas:** DVB e teletexto são copiadas para as saídas MPEG-TS.
- **DVR e timeshift:** gravação local no servidor, com verificação de integridade.
- **DRM:** HLS AES-128 com token.
- **EPG:** importação de XMLTV.
- **Autorização:** IP, token e backend HTTP.
- **Sessões:** sessões ao vivo e histórico de 30 dias, com uma página Sessões.
- **Painel:** alertas e estatísticas; configurações e marca editáveis no painel; editor de configuração em texto.
- **Cluster:** controller redundante com lease.
- **Documentação no painel:** em `/docs`.

### Regras do projeto (obrigatórias)

- **Não usar git** (nada de commit nem push): o repositório não é do usuário, e outra pessoa vai subir o código.
- **Não gravar DVR no PC do usuário.** Testes de DVR devem ser curtos e apagar o que gravarem; os canais de demonstração ficam com DVR desligado.
- **Whitelabel:** a marca nunca entra fixa no código. No painel, use `useBranding()`.
- **Segredos** nunca vão para o código nem para o repositório.
- **Sem números inventados:** capacidade, consumo e latência só podem ser afirmados com benchmark registrado.
- **Store e migrações:** mudanças no contrato `store.Store` exigem atualizar a implementação em memória, a do Postgres (com uma migração nova em `control-plane/store/postgres/migrations/`) e a suíte `storetest`.
- **OpenAPI em sincronia:** novas rotas da API entram em `control-plane/api/openapi.json` e em [docs/api.md](docs/api.md).
- **Verificação antes de entregar:**
  - `gofmt -l .`, `go vet ./...`, `go test ./...` e `golangci-lint run` (0 issues);
  - no painel: `npm run lint`, `npm run typecheck` e `npm run build`.

## 1. Validação em laboratório

Nada foi validado num servidor real até agora. O roteiro está em [docs/laboratorio.md](docs/laboratorio.md) e exige um servidor Linux de laboratório, não o PC de desenvolvimento.

| Fase | O que provar |
| --- | --- |
| 2 | Um canal estável por 24 h |
| 3 | Custo de transcodificação por perfil (CPU, QSV, NVENC) |
| 5 | DVR gravando por 72 h |
| 6 | Failover com quedas reais de fonte |
| 7 | Cluster com vários servidores e realocação de canais |
| 8 | Escala (meta de 1.000 canais), estabilidade e muitos espectadores |

O laboratório também precisa cobrir:

- **Matriz de compatibilidade:** testes com players reais (VLC, Smart TVs, Android/iOS e set-top boxes).
- **Custos do scheduler:** hoje são provisórios (Configurações › Cluster) e devem ser calibrados com os resultados da fase 3.

## 2. Decisões do responsável pelo projeto

Não implemente nada que dependa destes pontos sem confirmação.

- **Perguntas em aberto** (lista também no [CLAUDE.md](CLAUDE.md)):
  - Os 1.000 canais serão só recebidos ou também transcodificados?
  - Qual o bitrate médio e máximo por canal, e quantos espectadores simultâneos?
  - Quais protocolos de entrada e formatos de saída são obrigatórios na v1?
  - Qual a retenção do DVR e quais canais serão gravados?
  - Todos os canais terão fonte redundante?
  - Que hardware está disponível (CPU, GPU, RAM, rede e armazenamento)?
  - A distribuição será pelos próprios servidores de mídia ou por edge/CDN?
  - O núcleo de mídia será FFmpeg, GStreamer ou os dois?
- **ADRs:** de 0003 a 0013, em [docs/adr/](docs/adr/), todos estão como "Proposta" e precisam ser aprovados ou ajustados.
- **Comportamentos definidos no código:** revisar a tabela da seção 11.3 de [docs/architecture.md](docs/architecture.md). Ela trata, por exemplo, da troca manual para o backup, das novas tentativas depois de `FAILED`, do passthrough de HEVC e de quando os espectadores são contados.

## 3. Código para chegar ao nível do Flussonic

### Já combinado como próximo passo

1. **DVR com ABR.** ✅ Feito em 17/11/2026 (código em `media-engine/dvr/abr.go`, `media-engine/dvr/handler.go` e `data-plane/agent/runner.go`). Com `dvr.all_qualities` ligado (desligado por padrão, então nada muda nos canais existentes), o DVR grava todas as qualidades: a mais alta na pasta do canal e as outras em `v<N>/`. Há a opção no painel (aba DVR), na API e no editor de texto (`qualities=all`). O catch-up e o timeshift respondem uma playlist mestre com as qualidades gravadas no intervalo, e a retenção e a integridade cobrem todas. Testado com testes de unidade e com FFmpeg real (`TestFFmpegABRDVR`). **Falta:** validar o salto entre qualidades num player real no laboratório.
2. **Faixas de áudio alternativas no HLS,** para o player escolher o idioma (`EXT-X-MEDIA TYPE=AUDIO`). Hoje todas as faixas de áudio vão juntas para o MPEG-TS.
3. **LL-HLS:** HLS de baixa latência, com partial segments e preload hints.
4. **DRM comercial:** Widevine, FairPlay e PlayReady, via CENC/CBCS com servidor de licenças. Hoje existe só AES-128 com token (ADR 0008).
5. **Limite de sessões no cluster todo.** ✅ Feito em 17/11/2026. O limite por canal (`max_sessions`) já valia para o cluster, porque cada canal roda em um servidor só. O que valia por servidor era o limite por assinante (`X-Max-Sessions`), e agora os servidores trocam as contagens pelo NATS (`media-engine/sessions/share.go`). É aproximado: um assinante pode passar do limite por até 5 s, e um servidor mudo por 15 s deixa de contar. O `X-Unique` continua só por servidor. Testado com o barramento em memória; falta testar com 2 servidores reais no laboratório.

### O Flussonic tem e o NexoStream ainda não (confirmar o que entra)

- **Publish por RTMP:** ✅ Feito em 17/11/2026, com servidor RTMP próprio (`media-engine/ingest/rtmp.go`). O listener do ffmpeg não valida a chave (testado: aceita qualquer uma), então o servidor de mídia faz o handshake e os comandos em Go, confere app e chave em tempo constante **antes** de aceitar mídia, e só então entrega o FLV ao ffmpeg para remultiplexar em MPEG-TS. Um encoder por vez; health checks e lixo não derrubam o listener; chave mínima de 16 caracteres; chave mascarada para quem só lê. Testado com encoders ffmpeg reais (chave certa, errada, app errado, dois encoders, lixo) e de ponta a ponta pelo agente até o HLS. **Limite:** o RTMP não é cifrado; para internet, SRT com senha. Falta testar com OBS e encoders de hardware no laboratório.
- **Saída WebRTC,** para latência muito baixa.
- **Logo ou overlay** sobre o vídeo na transcodificação. ✅ Feito em 17/11/2026: posição, margem, largura e opacidade, em todas as qualidades do ABR; imagem só por nome de arquivo na pasta de logos do servidor; arquivo ausente não derruba o canal. Verificado com FFmpeg real (cor do pixel na saída) e no Chrome (o logo aparece no player, inclusive com o encoder QSV).
- **Exportar trechos do DVR em MP4.** ✅ Feito em 17/11/2026 (`media-engine/dvr/export.go`, `/dvr/<canal>/archive-<de>-<duração>.mp4`; ver docs/api.md). O painel tem o painel "Exportar em MP4" na aba DVR (início e duração, até 2 h); canais com DRM ficam de fora de propósito. Falta testar o arquivo num player real.
- **Marcadores SCTE-35** levados para o HLS, para inserção de anúncios.
- **Entrega por RTMP ou RTSP,** com o player puxando do servidor.

## 4. Instalação como pacote Linux

É um requisito para depois, e o responsável quer confirmar antes de começar.

O objetivo é instalar o NexoStream como pacote `.deb`: um serviço com tudo, painel aberto pelo IP da máquina e TLS configurado na instalação.

Hoje existem três unidades systemd separadas em `deploy/systemd/` e um `deploy/scripts/install.sh`.

## 5. Ajuste de documentação

Concluído em 17/11/2026. As linhas "ABR: um perfil por canal" e "Controller: instância única" saíram da seção 11.3 de [docs/architecture.md](docs/architecture.md), que já tinha as linhas corretas (ADR 0011 e ADR 0010). As mesmas afirmações também foram corrigidas em [docs/api.md](docs/api.md), no [README.md](README.md), no [CLAUDE.md](CLAUDE.md) e no comentário de `cmd/nexostream-controller`.

O [README.md](README.md) foi reescrito para o estado atual (protocolos, saídas, DVR, DRM, sessões, painel).

## 6. Endurecimento de segurança (17/11/2026)

### Feito

- **FFmpeg:** os pulls usam `-protocol_whitelist` por esquema (`media-engine/ingest/puller.go`). Uma playlist HLS hostil não lê mais arquivos locais. Há teste de regressão com o ffmpeg real.
- **IP do cliente na API:** `X-Real-IP` e `X-Forwarded-For` só valem quando a conexão vem de loopback ou de `NEXOSTREAM_TRUSTED_PROXIES`, e vale a última entrada do `X-Forwarded-For`. Antes, qualquer cliente forjava o IP e escapava do limite de login.
- **Login:** limite por usuário além do limite por IP, e as falhas vão para a auditoria (`auth.login_failed`).
- **Sessões:** `POST /auth/logout`; trocar senha ou perfil revoga os tokens emitidos antes (`users.tokens_valid_after`, migração 000011, testada em Postgres embarcado).
- **Senha:** de 10 a 72 caracteres, com pelo menos 5 diferentes e sem ser só uma palavra comum.
- **Cabeçalhos:** `Cache-Control: no-store` na API, HSTS atrás de TLS, COOP, Permissions-Policy e CSP no painel.
- **Rede:** TLS mínimo 1.2; EPG e backend de autorização não alcançam endereços link-local nem metadados de nuvem (`internal/netguard`).

### Segunda rodada (17/11/2026)

- **Refresh token rotativo:** cada token de renovação vale uma vez. Se um já usado reaparecer, a sessão inteira (a família) é revogada, com `401 token_reused` e `auth.refresh_reuse` na auditoria. Tabela `refresh_tokens` (migração 000013).
- **Bloqueio de conta:** 5 falhas seguidas bloqueiam por 5 min, dobrando a cada novo múltiplo, até 1 h. A recusa é igual à de senha errada, para não revelar se a conta existe. O administrador desbloqueia no painel.
- **TLS no worker:** `NEXOSTREAM_TLS_CERT_FILE` e `NEXOSTREAM_TLS_KEY_FILE` fazem o endereço de mídia servir HTTPS (TLS 1.2 ou mais novo), testado com handshake real. O worker avisa no log quando escuta na rede sem TLS.
- **Segredos no repositório:** `tests/architecture` falha se houver chave privada, token, senha em URL ou chave de nuvem no código. As senhas de desenvolvimento e de CI são públicas por declaração, e um teste garante que o compose de desenvolvimento só publica portas em `127.0.0.1`.
- **Dependências:** o CI passou a rodar `govulncheck` (versão fixada) e `npm audit`.

### Terceira rodada (17/11/2026)

- **Segredos mascarados para quem só lê:** senhas de fonte, `passphrase` SRT, chaves de stream (push e RTMP) e tokens nas URLs de autorização e EPG eram devolvidos sem máscara a qualquer usuário com `channels:read`, inclusive o perfil "somente leitura". Agora só `channels:write` os vê. Vale para `/channels`, `/channels/{id}`, `/sources` e a exportação `/config`; e o status do worker, os eventos, os logs e a auditoria saem mascarados (`internal/redact`, com testes).
- **Listener SRT exige senha:** a API recusa `mode=listener` sem `passphrase` de 10 a 79 caracteres. Canais já cadastrados continuam rodando; a regra vale na criação e na edição.
- **Métricas do worker:** o padrão de `NEXOSTREAM_METRICS_ADDR` passou a ser `127.0.0.1:9500`.

### Pendente

- **Painel no navegador:** testado no Chrome 157 sem janela, controlado pelo protocolo de depuração (17/11/2026): login, todas as páginas, as abas do canal, o player tocando HLS de outra origem com a CSP ativa, e o logo visível no vídeo. Resultado: **nenhuma violação de CSP e nenhum erro do painel no console**. Uma exceção intermitente veio de uma extensão do Chrome instalada na máquina, não do painel. Falta testar em Firefox e Safari.
- **`golangci-lint` v2.15.0** (a mesma versão do CI) rodou aqui: **0 problemas**, depois de corrigir os que ele achou no código novo.
- **`govulncheck` e `npm audit` ainda não rodaram aqui:** o classificador de permissões bloqueou o `govulncheck`, mesmo com a versão fixada. Eles rodam no CI; para rodar localmente, libere o comando nas permissões ou rode `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` você mesmo.
- **Perda da resposta de uma renovação** (rede ruim) exige novo login, porque o token de renovação vale uma vez e não há janela de tolerância.
- O painel guarda o token em `sessionStorage` (a CSP reduz o risco de XSS, mas não o elimina).
- O endereço de métricas do worker (`NEXOSTREAM_METRICS_ADDR`) fica em HTTP puro e sem autenticação, por desenho (rede de gerência). O padrão agora é `127.0.0.1:9500`: para o Prometheus de outra máquina, defina o IP da rede de gerência (como em `deploy/configs/worker.env.example`) e restrinja por firewall.
- Sem revisão externa de segurança nem teste de intrusão.
