# 0006 — DVR local no servidor de mídia, no modelo do Flussonic

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

A especificação (seções 7 e 13) sugeria um índice de segmentos no PostgreSQL. Na prática de operação de IPTV (Flussonic e similares), o DVR fica no próprio servidor: o operador aponta a pasta ou os discos onde cada servidor grava, e o servidor serve timeshift e catch-up. Um índice central acrescenta tráfego (um registro a cada segmento de cada canal) e um ponto de falha para algo que é local por natureza.

## Decisão

- Cada Media Node grava os segmentos HLS que já produz em **um arquivo por hora** por canal: `<pasta>/<canal>/AAAA/MM/DD/HH.ts` (MPEG-TS contínuo, abre direto no VLC), com um índice ao lado, `HH.idx` (uma linha por trecho: início, duração, offset, tamanho).
- As playlists de timeshift e catch-up apontam para pedaços do arquivo da hora com `#EXT-X-BYTERANGE`, então dá para buscar qualquer momento sem dividir o arquivo. Depois de uma queda, o arquivo da hora é cortado de volta até o último trecho indexado.
- Onde gravar, por canal: um armazenamento nomeado (discos + modo), uma pasta específica, ou a pasta padrão do servidor (`NEXOSTREAM_DVR_DIR`).
- Modos do armazenamento: `single`; `raid0`, em que cada hora inteira vai para um disco, escolhido por hash da hora; e `raid1`, que espelha cada hora em todos os discos. Um disco com falha é marcado e evitado por 1 min (modo degradado).
- O nó serve `/dvr/<canal>/index.m3u8?from=&to=`, `index-<início>-<duração|now>.m3u8` (estilo Flussonic), `recordings.json`, os segmentos e a exportação de trechos em MP4 (`archive-<início>-<duração>.mp4`, cópia de pacotes, até 2 h, sem DRM). Em canal ABR com `all_qualities`, as mesmas URLs respondem uma playlist mestre, e cada qualidade fica em `/dvr/<canal>/v<N>/...` ([ADR 0011](0011-abr-com-um-decode.md)). A retenção é aplicada no nó, apagando arquivos de hora inteiros.
- O Control Plane só guarda a configuração (política e armazenamentos) e consulta o nó quando a API precisa dos intervalos gravados.

## Consequências

- **Mais fácil:** operação familiar; nenhuma dependência central para gravar ou assistir; escala junto com os nós.
- **Mais difícil:** gravações de um canal que muda de nó ficam no nó antigo. Mitigação: armazenamento compartilhado (NFS/SAN) montado no mesmo caminho em todos os nós, ou fixar o canal no nó (afinidade, futura).
- O RAID é feito pela aplicação, por segmento. RAID de hardware ou mdadm continuam possíveis: basta um armazenamento `single` apontando para o volume.

## Referências

- Especificação, seção 7; [architecture.md](../architecture.md), seção 11.
