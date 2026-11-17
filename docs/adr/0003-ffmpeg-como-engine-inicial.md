# 0003 — FFmpeg como engine de mídia inicial, atrás de uma interface

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

O ADR 0002 decidiu integrar GStreamer e FFmpeg, mas deixou em aberto qual usar em cada etapa (decisão pendente da seção 21 da especificação). O MVP precisa de remux, transcodificação H.264/AAC em CPU, NVENC e Quick Sync, saída HLS e MPEG-TS, e diagnóstico (`ffprobe`), de preferência com uma única ferramenta para reduzir a superfície de integração.

## Decisão

- Implementar a primeira engine com **FFmpeg**: um processo `ffmpeg` por canal, com entrada MPEG-TS pelo `stdin` e todas as saídas pelo muxer `tee`, para que vídeo e áudio sejam decodificados e codificados uma única vez.
- Esconder a engine atrás da interface `pipeline.Engine` (`media-engine/pipeline/engine.go`). O agente não conhece o FFmpeg, só a interface.
- Usar `ffprobe` sobre alguns segundos capturados da fonte para detalhes que o PMT não informa (resolução, FPS, entrelaçamento).
- Validar NVENC e Quick Sync com uma codificação curta de teste na inicialização do worker, e não apenas pela presença do encoder no build do FFmpeg.

## Alternativas consideradas

- **GStreamer primeiro:** mais controle do pipeline e das estatísticas, mas exige mais código para transcodificação com aceleração e para empacotamento HLS com as mesmas garantias.
- **As duas engines desde o início:** dobra o custo de testes antes de existir benchmark que justifique.

## Consequências

- **Mais fácil:** um caminho único para passthrough, conversão só de áudio e transcodificação; comandos reproduzíveis no laboratório (os argumentos estão em `pipeline.BuildArgs`).
- **Mais difícil:** um processo por canal custa memória; a troca de fonte reinicia o processo (~1 s de interrupção, marcada com `#EXT-X-DISCONTINUITY` no HLS).
- **Próximos passos:** benchmark da Fase 3 (custo por perfil em CPU e GPU); avaliar uma engine GStreamer para passthrough em grande escala se o consumo por processo for limitante. A decisão final vira um novo ADR que substitui este.

## Referências

- [0002](0002-integrar-engines-de-midia-existentes.md); especificação, seções 4, 6 e 21.
- `media-engine/pipeline/ffmpeg.go`, `media-engine/transcoding/transcoding.go`.
