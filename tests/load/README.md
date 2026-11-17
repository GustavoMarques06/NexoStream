# Testes de carga

Ferramentas para as etapas de escala da especificação (seção 19.3): 10 → 50 → 100 → 250 → 500 → 1.000 canais. Só avance para a próxima etapa depois que a anterior ficar estável.

> **Regra do projeto:** nenhum número de capacidade vale sem um registro em `resultados/`, feito com o [modelo](resultados/MODELO.md). Os scripts não "provam" capacidade; eles só geram carga e coletam medidas.

## Pré-requisitos

- Servidor de laboratório representativo do hardware de produção (ver `docs/deployment.md`, seção 2).
- `nexostream-*` instalados, `ffmpeg`, `curl` e `jq`.
- Um token de integração com `channels:write` e `channels:operate` (painel → API e tokens).
- Para cargas acima de algumas dezenas de canais, gere as fontes em **outra máquina**, para não disputar CPU com o nó medido.

## 1. Gerar fontes de teste

```bash
# 50 fontes H.264/AAC 1080p25 a ~4 Mbps, multicast 239.100.0.1..50, porta 5000
./gen-sources.sh 50 239.100.0 5000 1920x1080 4000k
# MPEG-2/MP2 para testar transcodificação:
VCODEC=mpeg2video ACODEC=mp2 ./gen-sources.sh 10 239.101.0 5000 720x576 4000k
```

Cada fonte é um `ffmpeg` com `testsrc2` e um tom de áudio. Os PIDs ficam em `sources.pids`; pare tudo com `./gen-sources.sh stop`.

Para testar com conteúdo real, prefira gravações de streams reais repetidas com `ffmpeg -re -stream_loop -1 -i gravacao.ts -c copy -f mpegts udp://...`. Testes só sintéticos não bastam (princípio de engenharia da especificação).

## 2. Cadastrar canais

```bash
export NEXOSTREAM_API=https://panel.nexostream.local/api/v1
export NEXOSTREAM_TOKEN=nxs_...
./create-channels.sh 50 239.100.0 5000 passthrough        # modo passthrough
./create-channels.sh 10 239.101.0 5000 transcode hd        # transcodificação com perfil hd
./create-channels.sh delete 50                             # parar e remover carga-001..050
```

## 3. Coletar medidas

```bash
./collect.sh 10.10.0.21:9500 3600 > resultados/raw/$(date +%F)-passthrough-50.csv
```

Amostra a cada 15 s: canais no ar, em falha e em backup, bitrate de entrada, erros CC, reinícios, entrada descartada e CPU/memória do processo do worker. CPU, rede e disco do nó vêm do `node_exporter` e do Grafana (dashboard "NexoStream — Visão geral").

Verifique também, durante o teste:

```bash
netstat -su | grep -i -E "receive|buffer"   # RcvbufErrors crescendo = buffer UDP insuficiente
```

## 3b. Espectadores

`nexostream-loadplay` simula players HLS. Cada espectador lê a playlist como um player, baixa cada segmento novo e mede o tempo do download em relação à duração do segmento. Quando o download demora mais que o segmento, o player travaria.

```bash
go build -o bin/ ./cmd/nexostream-loadplay
./bin/nexostream-loadplay -url 'https://origem/canal-001/index.m3u8?token=…' -viewers 200 -ramp 60s -duration 30m -report 30s > resultados/raw/espectadores.csv
```

Rode a partir de várias máquinas que não sejam o servidor medido. Para canais ABR, `-variant 1` toca a segunda qualidade.

## 4. Registrar

Copie `resultados/MODELO.md` para `resultados/AAAA-MM-DD-<cenario>.md` e preencha. Inclua hardware, versões (NexoStream, FFmpeg, driver de GPU), parâmetros, duração e conclusão.

## Cenários obrigatórios por etapa

| Cenário | Como |
| --- | --- |
| Só passthrough | `create-channels.sh N ... passthrough` |
| Só transcodificação | `create-channels.sh N ... transcode hd` (CPU) e com perfil `encoder: nvenc`/`qsv` |
| Com DVR | Fase 5 |
| Espectadores | `nexostream-loadplay` contra o nginx de origem, a partir de outras máquinas |
| Matriz de codecs | `go test ./tests/integration/ -run TestCompatMatrix -v` (software), mais players reais |

O roteiro completo, fase por fase, está em [docs/laboratorio.md](../../docs/laboratorio.md).
