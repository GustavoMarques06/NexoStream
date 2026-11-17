# 0011 — ABR: várias qualidades com uma decodificação

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

Na v1, cada canal tinha um único perfil de transcodificação. Com uma qualidade só, um espectador com pouca banda trava e não tem para onde descer. A especificação prevê "múltiplas resoluções e bitrates" (§6), e o Flussonic oferece isso na aba Transcoder.

## Decisão

- **Configuração:** no modo `transcode`, o canal aceita de 1 a 6 perfis (`profiles`). Com dois ou mais, vira ABR. No editor de texto: `transcode fhd hd sd;`.
- **Pipeline:** um único processo FFmpeg por canal.
  - O vídeo é decodificado uma vez e dividido (`split`) em uma cadeia de escala por qualidade, cada uma com seu encoder.
  - O áudio é codificado uma vez e entra em todas as qualidades, com todas as trilhas.
  - As qualidades ficam em ordem decrescente de resolução: `v0` é a mais alta.
- **Saída HLS:**
  - `<canal>/v<N>/index.m3u8` e os segmentos de cada qualidade, com GOP fixo, para os segmentos ficarem alinhados entre qualidades;
  - `<canal>/index.m3u8` é a playlist mestre, gravada pelo worker, com `BANDWIDTH`, `AVERAGE-BANDWIDTH`, `RESOLUTION` e `CODECS`.
- **Quem usa a qualidade mais alta:** o MPEG-TS por HTTP e por UDP e a miniatura.
- **DVR:** por padrão grava só a mais alta. Com `dvr.all_qualities` ligado, grava todas. A mais alta fica na pasta do canal, como antes, e as outras em `<canal>/v<N>/`, com o mesmo formato de hora e índice. O arquivo `.variants.json` guarda a escada gravada. O catch-up e o timeshift respondem uma playlist mestre com as qualidades gravadas no intervalo pedido (`v<N>/index.m3u8` para cada uma). A retenção e a verificação de integridade cobrem todas.
- **DRM e autorização:** valem para a playlist mestre e para cada qualidade. O token é repetido nas URIs de todas.
- **Scheduler:** o custo de um canal ABR é a soma do custo de cada qualidade, e o servidor precisa ter os encoders de todas.
- **Troca de layout:** ao mudar de uma qualidade para ABR, ou o contrário, a pasta do canal é recriada (arquivo `.layout`), porque o empacotador continua playlists existentes.

## Consequências

- **Mais fácil:**
  - **Reprodução:** o player escolhe a qualidade pela banda.
  - **Custo:** uma decodificação para N qualidades sai mais barato que N processos.
- **Mais difícil:**
  - **CPU/GPU:** o consumo cresce com o número de qualidades. A capacidade por servidor precisa ser medida no laboratório, com a escada real.
  - **Disco do DVR:** com `all_qualities` ligado, cresce com a soma das qualidades gravadas. O espaço precisa ser dimensionado com a escada real.
  - **Uma falha derruba todas:** um erro em qualquer encoder derruba o processo inteiro, com todas as qualidades.

    Para o encoder de hardware existe uma proteção. Quando o QSV ou o NVENC falham, ficam 30 min de quarentena, e os perfis `auto` voltam em CPU (evento `worker.encoder_failed`). Isso foi observado no PC de desenvolvimento: o driver do Quick Sync respondeu "device failed" depois de cerca de 50 s, e o canal voltou sozinho em CPU.
- **Comprovado só no PC de desenvolvimento:** a escada com 2 qualidades em CPU, gerando a mestre e as variantes. O ffprobe confirmou a resolução de cada qualidade.
