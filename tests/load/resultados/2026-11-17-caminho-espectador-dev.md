# Caminho do espectador — PC de desenvolvimento

> **Escopo:** primeira leitura das métricas `nexostream_play_*` com carga sintética. Foi feita num PC de desenvolvimento com Windows 11, não no servidor de laboratório. **Não mede capacidade** e não serve para dimensionar: o Windows não tem o `sendfile` do Linux, a carga e o servidor estavam na mesma máquina (loopback), e o modo dev roda API, controller e worker no mesmo processo.

- **Data:** 2026-11-17
- **Ambiente:**
  - Windows 11 Pro;
  - FFmpeg 9.0.2 (build full da Gyan);
  - `nexostream-dev` em `127.0.0.1`, dados em memória.
- **Canal:** `e2e`, passthrough, só HLS, sem DVR, sem autorização (a admissão não consulta backend).
- **Fonte:** `testsrc2` 640×360, 25 fps, H.264 a 1,2 Mbps e AAC a 96 kbps, por UDP/MPEG-TS.
- **Carga:** `nexostream-loadplay -viewers 50 -ramp 10s -duration 60s -report 20s`, na mesma máquina.
- **Como foi medido:** `/metrics` do worker lido antes e depois da carga; os valores abaixo são as diferenças.

## Resultado

| Medida | Valor | Origem |
| --- | --- | --- |
| Pedidos | 1532, todos 2xx | `nexostream_play_requests_total` (igual ao total do loadplay) |
| Erros | 0 | loadplay e métrica |
| Segmentos lentos (download maior que a duração do segmento) | 0 de 592 | loadplay |
| Bytes enviados | 620,5 MB em cerca de 60 s (média de ~83 Mbps) | `nexostream_play_sent_bytes_total` |
| Tempo médio de resposta | 1,93 ms por pedido | `nexostream_play_request_duration_seconds` (soma / contagem) |
| Tempo médio de admissão | ~15 µs por pedido | `nexostream_play_admission_duration_seconds` |
| CPU do processo Go | 3,75 s em ~60 s (cerca de 6 % de um núcleo) | `process_cpu_seconds_total` |

Por intervalo, o loadplay mostrou 106,4, 77,5 e 64,3 Mbps. A queda reflete o começo: cada espectador novo baixa de uma vez os segmentos que já estão na playlist, e depois segue no ritmo do canal.

## O que isto mostra e o que não mostra

- **Mostra:** as métricas novas funcionam e batem com o lado do cliente (mesmo número de pedidos), e o caminho do espectador respondeu sem erro e sem segmento lento nessa carga.
- **Não mostra:** quanto um servidor aguenta. Para isso, repetir no laboratório (Linux, carga vinda de outras máquinas, canais com autorização por backend) e subir os espectadores até a placa de rede ou o CPU saturar, como descreve `docs/laboratorio.md`.
