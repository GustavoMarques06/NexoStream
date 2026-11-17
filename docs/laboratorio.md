# Roteiro de laboratório

> Este roteiro transforma os critérios de conclusão do plano de desenvolvimento (especificação §18) e os testes de validação (§19) em procedimentos. Cada etapa só conta como concluída com um registro em `tests/load/resultados/`, feito com o [modelo](../tests/load/resultados/MODELO.md). Nenhum número de capacidade vale sem esse registro.

## Sumário

0. [Preparação](#0-preparação)
1. [Fase 2: um canal estável por 24 h](#fase-2-um-canal-estável-por-24-h)
2. [Fase 3: custo de transcodificação por perfil](#fase-3-custo-de-transcodificação-por-perfil)
3. [Fase 5: DVR de 72 h](#fase-5-dvr-de-72-h)
4. [Fase 6: failover com quedas reais](#fase-6-failover-com-quedas-reais)
5. [Fase 7: cluster e realocação](#fase-7-cluster-e-realocação)
6. [Fase 8: escala, estabilidade e espectadores](#fase-8-escala-estabilidade-e-espectadores)
7. [Matriz de compatibilidade](#matriz-de-compatibilidade)
8. [Players reais](#players-reais)

## 0. Preparação

- **Hardware:** um servidor representativo do de produção, com CPU, GPU e NICs registradas no relatório.
- **Instalação:** pelo [deployment.md](deployment.md), com `install.sh`, systemd e nginx. A instalação faz parte do teste.
- **Gerador de fontes:** uma segunda máquina rodando `tests/load/gen-sources.sh`, para não disputar CPU com o servidor medido.
- **Dados de monitoramento:**
  - Grafana com o dashboard "NexoStream — Visão geral";
  - a página Estatísticas do painel (uma amostra por minuto);
  - a página Alertas aberta durante o teste.
- **Conteúdo real:** além das fontes sintéticas, gravações de canais reais repetidas com `ffmpeg -re -stream_loop -1 -i gravacao.ts -c copy -f mpegts udp://…`.

## Fase 2: um canal estável por 24 h

**Critério:** um canal em HLS e MPEG-TS, estável por 24 h.

1. Crie um canal UDP passthrough com HLS e MPEG-TS por HTTP.
2. Rode `nexostream-loadplay -url …/index.m3u8 -viewers 5 -duration 24h -report 60s` de outra máquina. Deixe também um VLC tocando o `stream.ts`.
3. Registre:
   - reinícios do pipeline (deve ser 0);
   - erros CC;
   - `RcvbufErrors` (`netstat -su`);
   - memória do worker no início e no fim (vazamento);
   - segmentos lentos no loadplay (deve ser 0).

## Fase 3: custo de transcodificação por perfil

**Critério:** perfis `hd` e `sd` com consumo de CPU e GPU medido por canal.

1. Para cada perfil (`sd`, `hd`, `fhd`, e as escadas ABR que forem usadas):
   - em CPU, adicione canais de 1 em 1 com `create-channels.sh N … transcode <perfil>` até a CPU passar de 80% ou o `speed` do FFmpeg cair abaixo de 1.0;
   - repita com `encoder: nvenc` ou `qsv`, se houver GPU.
2. Rode cada configuração por pelo menos 30 min. Fique atento ao evento `worker.encoder_failed`: no PC de desenvolvimento, o Quick Sync falhou depois de cerca de 50 s, e o canal voltou sozinho em CPU.
3. Com os números medidos, ajuste em Configurações › Cluster os custos do scheduler (passthrough, por megapixel, desconto de GPU).

## Fase 5: DVR de 72 h

**Critério:** gravar por 72 h e reproduzir por horário.

1. Crie um armazenamento RAID-1 com dois discos e coloque 10 canais gravando, com retenção de 3 dias.
2. Durante o teste:
   - toque o timeshift e o catch-up (`index-<de>-<duração>.m3u8`) pelo painel e pelo VLC;
   - desconecte um disco e confirme que a gravação continua no outro e que o alerta `dvr_disk` aparece.
3. No fim, registre:
   - espaço usado contra o estimado (aba DVR do canal);
   - eventos `dvr.integrity_error` (deve ser 0);
   - a limpeza automática das horas além da retenção.

## Fase 6: failover com quedas reais

**Critério:** failover automático validado derrubando fontes.

1. Use 10 canais com fonte principal e backup em multicast de origens diferentes.
2. Para cada cenário, registre o tempo até a troca (eventos `channel.source_switched`) e se a playlist continuou sem travar o player:
   - derrubar a fonte principal (parar o gerador);
   - bitrate baixo (`-b:v` muito menor);
   - sumir o áudio (gerador sem `-map 0:a`);
   - erros CC (`tc netem loss 5%` na interface de ingestão).
3. Confirme a volta para a principal depois do tempo de estabilidade.

## Fase 7: cluster e realocação

**Critério:** dois ou mais servidores, com os canais realocados automaticamente depois de uma falha.

1. Monte 2 ou 3 workers e 2 controllers ([ADR 0010](adr/0010-controller-redundante-com-lease.md)).
2. Teste cada situação:
   - **Desligar um worker:** os canais devem migrar em até ~15 s (alerta `worker_offline`, evento `worker.channels_reassigned`).
   - **Matar o controller líder:** o outro deve assumir em até 15 s ("now the leader" no log), e a realocação deve continuar.
   - **Drain de um worker:** a migração deve ser gradual.
   - **Papéis:** com um nó só `transcode` e outro só `passthrough`/`dvr`, confira se o scheduler respeita.

## Fase 8: escala, estabilidade e espectadores

**Critério:** as metas da §19 atingidas e documentadas.

1. **Escala:** carga progressiva de 10, 50, 100, 250, 500 e 1.000 canais, em três cenários: passthrough, transcodificação e passthrough + DVR. Avance de etapa só depois de 1 h estável na anterior.
2. **Estabilidade:** testes contínuos de 72 h e de 7 dias na maior carga estável. Durante esses testes, inclua:
   - reinício controlado de um worker;
   - reinício do servidor inteiro;
   - falha de rede.
3. **Espectadores:** carga separada com `nexostream-loadplay` rodando em várias máquinas, subindo até a banda de saída prevista. Registre:
   - segmentos lentos (p95 de download/duração abaixo de 0.5);
   - erros;
   - CPU do worker, que agora serve o HLS ([ADR 0009](adr/0009-sessoes-autorizacao-e-epg.md)).

   **Para decidir entre servir pelos próprios servidores ou por edge/CDN**, use as métricas do caminho do espectador, que existem por protocolo (`proto` = `hls`, `dvr` ou `mpegts`) e não por canal. Suba os espectadores aos poucos e anote, para cada degrau:
   - `rate(nexostream_play_requests_total[1m])` por `class`: pedidos por segundo e, em especial, o aparecimento de `5xx`;
   - `histogram_quantile(0.95, rate(nexostream_play_request_duration_seconds_bucket[1m]))`: tempo de resposta de playlists e segmentos;
   - `histogram_quantile(0.95, rate(nexostream_play_admission_duration_seconds_bucket[1m]))`: custo de admitir o pedido (regras, sessão e backend de autorização), que é o que a CDN não elimina;
   - `rate(nexostream_play_sent_bytes_total[1m]) * 8`: bits por segundo entregues, para comparar com a placa de rede;
   - `nexostream_play_inflight_requests` e o CPU do processo (`process_cpu_seconds_total`).

   O ponto de decisão é o degrau em que o p95 de resposta ou de admissão dispara, ou o CPU do worker satura, **antes** de a placa de rede saturar. Se isso acontecer, o Go é o limite e vale tirar os bytes dele (edge/CDN ou nginx na frente). Se a placa saturar primeiro, não há ganho em mudar. Não há número de referência: ele sai desta medição, registrada em `tests/load/resultados/`.

## Matriz de compatibilidade

O teste automatizado `go test ./tests/integration/ -run TestCompatMatrix -v` confirma, com o FFmpeg instalado, que cada combinação de entrada sai em HLS H.264 + AAC e com a decisão certa (repassar, converter o áudio ou transcodificar). Ele não substitui o teste com sinais e players reais. O resultado no PC de desenvolvimento está em [`tests/load/resultados/2026-11-16-matriz-codecs-dev.md`](../tests/load/resultados/2026-11-16-matriz-codecs-dev.md).

## Recursos novos que só o laboratório valida

Já testados com ffmpeg real e no Chrome (ver `go test ./tests/integration/`), mas ainda não com equipamento real:

- **Publish por RTMP** (`rtmp://0.0.0.0:1935/<app>/<chave>?mode=listener`): publicar com OBS, vMix e um encoder de hardware. Conferir que uma chave errada é recusada (`NetStream.Publish.BadName`), que o canal volta sozinho quando o encoder cai e reconecta, e que um health check de balanceador na porta não atrapalha.
- **Publish por SRT com senha:** o mesmo, com um encoder SRT de hardware.
- **Logo (overlay):** conferir posição e transparência numa Smart TV e num set-top box, em cada qualidade do ABR, e o custo extra de CPU ou GPU no benchmark da fase 3.
- **Exportação em MP4:** abrir o arquivo exportado no VLC, num celular e num editor de vídeo.

## Players reais

Para cada tipo de aparelho (set-top box, Smart TV Samsung e LG, Android, iOS e tvOS, navegador), teste:

- HLS simples;
- ABR, trocando de qualidade ao limitar a banda;
- DRM AES-128 com token;
- timeshift e catch-up;
- MPEG-TS por HTTP.

Registre modelo, versão do firmware e resultado na matriz da especificação (§5.4).
