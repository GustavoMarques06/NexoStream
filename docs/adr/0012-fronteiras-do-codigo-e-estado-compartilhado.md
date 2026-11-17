# 0012 — Fronteiras do código, estado compartilhado e versão do contrato

- **Status:** Proposta
- **Data:** 2026-11-17
- **Responsáveis:** equipe NexoStream

## Contexto

O [ADR 0001](0001-separar-data-plane-e-control-plane.md) separa o Data Plane (os servidores de mídia) do Control Plane (API e controller) para os canais continuarem no ar sem o controle. Uma revisão do código achou pontos em que o código não seguia isso, ou em que o desenho não aguentava vários servidores:

- O agente do servidor de mídia ficava em `control-plane/workers`, e o binário do worker carregava o driver do PostgreSQL e o `store`, só porque as funções que abrem o banco e o Redis estavam junto dos utilitários comuns de todos os binários.
- O limite de tentativas de login e o registro de idempotência ficavam na memória de cada instância da API. Com N instâncias, o limite virava N vezes maior e uma repetição que caísse em outra instância executava de novo.
- O pacote `dvr` guardava o estado de gravação (horas abertas, discos com falha, exportações) em variáveis globais.
- O controller e o worker não tinham como saber que falavam versões diferentes do contrato de mensagens.
- O contrato `store.Store` tinha mais de quarenta métodos num só bloco, e a gravação do DVR rodava dentro do ciclo de métricas do `runner`.

## Decisão

- **Pasta do Data Plane:** o agente mora em `data-plane/agent`. O Data Plane (`data-plane/`, `media-engine/`) não importa nada de `control-plane/`, e o Control Plane não importa `data-plane/`. O vocabulário comum fica em `internal/`.
- **Binário do worker sem banco:** as funções `OpenPostgres` e `OpenRedis` saíram de `internal/runtime` para `control-plane/boot`. O binário do worker não carrega mais o driver do PostgreSQL nem o do Redis.
- **Teste das fronteiras:** `tests/architecture` falha se alguma dessas regras for quebrada, inclusive se o binário do worker voltar a carregar código do banco.
- **Estado compartilhado da API:** o limite de login (por IP e por usuário) e as respostas de idempotência ficam no Redis (`control-plane/sharedstate`), então valem para todas as instâncias da API. A janela do limite é de 1 minuto. Sem Redis (modo dev), ou durante uma queda dele, cada instância usa a própria memória: o cliente tem timeout curto e um disjuntor de 10 s evita esperar o Redis a cada requisição.
- **Estado do DVR por agente:** `dvr.State` guarda o que antes era global. Cada agente tem o seu. Um `Storage` sem `State` usa um padrão do processo, para ferramentas e testes simples.
- **Versão do contrato:** o heartbeat do worker traz `protocol.Version`. O controller avisa no log, uma vez por worker e versão, quando ela difere da sua. Subir o número quando uma mudança não for compatível com a versão anterior.
- **Engine de mídia como contrato executável:** `pipelinetest.RunConformance` descreve o que o agente espera de um `pipeline.Engine` (parar a pedido sem erro, `Stop` repetido, escrita que falha depois do fim, cancelamento do contexto, métodos seguros em paralelo, progresso e HLS reais). A engine FFmpeg e a engine falsa passam. A suíte achou que o FFmpeg ignorava o cancelamento do contexto, e isso foi corrigido.
- **Caminho do espectador medido:** o worker expõe, por protocolo, pedidos por classe de status, tempo de resposta, tempo de admissão, bytes enviados e pedidos em andamento (`nexostream_play_*`). O invólucro que conta os bytes passou a repassar o `ReadFrom`, para o `sendfile` do kernel não ser escondido. Com isso, a decisão sobre edge/CDN passa a ter dados (ver `docs/laboratorio.md`).
- **Gravação do DVR no runner:** a gravação virou um componente (`dvrRecorder`) com passo próprio no laço do runner, independente das métricas.
- **Contrato do `store`:** dividido por assunto (`ChannelStore`, `UserStore`, `SettingsStore` e outros), e `Store` é a composição deles. Os componentes declaram só o que usam.

## Consequências

- **Mais fácil:**
  - rodar mais de uma instância da API;
  - testar partes isoladas, com um `State` próprio ou uma interface pequena do `store`;
  - perceber um cluster com versões misturadas.
- **Mais difícil:**
  - **Limite de login aproximado:** a janela fixa de 1 minuto deixa um cliente usar o limite no fim de um minuto e de novo no começo do seguinte.
  - **Queda do Redis:** durante ela, o limite volta a valer por instância. Isso é registrado no log.
  - **Aviso, não bloqueio:** a diferença de versão do contrato só gera aviso. Recusar o worker seria uma decisão a tomar com a política de atualização do cluster.
- **Ainda não feito, e por quê:**
  - **HLS pelo Go do worker:** continua assim. Falta medir (as métricas existem) e decidir sobre edge/CDN.
  - **Um processo FFmpeg por canal, com todas as qualidades do ABR nele:** continua assim. Separar em processos custa decodificações a mais, e isso só se decide com o benchmark da fase 3.
  - **Uma segunda engine de mídia:** não existe. O critério para aceitar uma é passar em `RunConformance`; falta a decisão sobre GStreamer.
