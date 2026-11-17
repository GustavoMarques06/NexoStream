# 0004 — Ingestão UDP e análise MPEG-TS no próprio agente

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

O failover precisa de indicadores confiáveis por fonte: presença de pacotes, bitrate e média móvel, erros de continuidade, evolução do PCR e presença de áudio e vídeo (especificação, seção 8.1). Se a engine de mídia lesse a fonte diretamente, essas medidas dependeriam do log ou do progresso da engine, e seria preciso abrir uma segunda conexão para monitorar a fonte principal enquanto o canal roda no backup.

## Decisão

- O agente recebe o UDP (com suporte a cabeçalho RTP) e analisa o MPEG-TS em Go: PAT/PMT com CRC, codecs por `stream_type` e descritores, contadores de continuidade e PCR (`media-engine/ingest`).
- O agente repassa os pacotes da fonte ativa ao `stdin` da engine por uma fila limitada. Quando a fila enche, descarta e conta (`nexostream_channel_input_dropped_bytes_total`), sem bloquear a recepção.
- A decisão passthrough/transcodificação usa o PMT, antes de iniciar a engine.
- A máquina de estados (`media-engine/health`) é pura e recebe tempo e medidas como entrada, o que permite testá-la sem streams reais.
- Em Linux, sockets multicast são associados ao endereço do grupo, para que canais com grupos diferentes na mesma porta não recebam o tráfego um do outro.

Isso não é um motor de codecs: o agente só lê cabeçalhos de transporte e tabelas PSI. Decodificação e codificação continuam no FFmpeg (ADR 0002).

## Alternativas consideradas

- **Engine lê a fonte e o agente monitora pelo log:** menos código, mas medidas pobres e sem acompanhamento da principal durante o backup.
- **TSDuck como analisador externo:** maduro, mas é mais um processo por canal e por fonte.

## Consequências

- **Mais fácil:** failover com medidas diretas; métricas por canal; testes determinísticos com gerador TS sintético (`media-engine/ingest/tstest`).
- **Mais difícil:** o agente fica no caminho dos dados. Se ele cair, os pipelines do nó caem junto (o kernel encerra o `ffmpeg` com `PDEATHSIG`) e o systemd reinicia o agente, que restaura os canais do estado local.
- **A validar no laboratório:** custo de CPU da análise em Go com centenas de canais por nó; MPTS (hoje é usado o primeiro programa do PAT); SSM (IGMPv3) ainda não é suportado.

## Referências

- Especificação, seções 8.1 a 8.5; [architecture.md](../architecture.md), seção 11.
