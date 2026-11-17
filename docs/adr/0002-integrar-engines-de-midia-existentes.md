# 0002 — Integrar engines de mídia existentes em vez de criar um motor de codecs

- **Status:** Aceita
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

Receber, analisar, remuxar, transcodificar e empacotar vídeo exige lidar com protocolos, contêineres e codecs complexos (MPEG-TS, H.264, HEVC, MPEG-2, AAC, AC-3), além de aceleração por hardware (NVENC, Quick Sync).

Construir esse motor do zero levaria anos, com alto risco de incompatibilidades e falhas difíceis de diagnosticar. Ferramentas maduras como GStreamer e FFmpeg já resolvem esses problemas e são usadas amplamente em broadcast e streaming.

O diferencial do NexoStream não está no codec, e sim na operação: orquestração, failover, observabilidade, gerenciamento e escala.

## Decisão

- Usar **GStreamer** e **FFmpeg** como base do processamento de mídia.
- Concentrar o desenvolvimento próprio em orquestração, gerenciamento, automação, observabilidade e experiência operacional.
- Considerar componentes próprios em C++ ou Rust **somente** quando benchmarks mostrarem um limite concreto de desempenho, memória ou integração.
- A escolha entre GStreamer, FFmpeg ou uma combinação dos dois para cada etapa do pipeline fica em aberto e será definida em um ADR próprio após os protótipos da Fase 2.

## Alternativas consideradas

- **Motor próprio de codecs e contêineres:** controle total, mas custo e risco muito altos para o prazo do projeto.
- **Media server comercial com integração por cima:** entrega rápida, mas mantém a dependência que o projeto quer eliminar e limita o controle sobre failover e escala.

## Consequências

- **Mais fácil:** suporte amplo a formatos e aceleração por hardware desde o início; foco da equipe no que diferencia o produto.
- **Mais difícil:** depender do comportamento e das versões dessas bibliotecas; entender bem seus modos de falha para supervisioná-las; acompanhar licenças (FFmpeg, x264 e codecs com patente como AC-3 e HEVC).
- **Próximos passos:** protótipos de ingestão UDP/MPEG-TS → HLS com as duas ferramentas na Fase 2; matriz de compatibilidade de codecs; revisão de licenças antes da produção.

## Referências

- [Especificação do projeto](../../../NexoStream_Projeto.md), seções 4 e 6.
- [architecture.md](../architecture.md), seção 2.
