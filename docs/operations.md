# Operação

Procedimentos para operar, diagnosticar e testar o NexoStream.

> **Status:** API, painel e serviços `nexostream-*` existem no MVP; os procedimentos abaixo já se aplicam, mas ainda não foram exercitados em laboratório. As regras de alerta estão em [`deploy/configs/prometheus/rules/nexostream.yml`](../deploy/configs/prometheus/rules/nexostream.yml) e os scripts de carga em [`tests/load/`](../tests/load/README.md). Comportamentos definidos na implementação (retenção da troca manual, nova tentativa após `FAILED` etc.) estão em [architecture.md, seção 11.3](architecture.md#113-comportamentos-definidos-na-implementação).

## Sumário

1. [Estados do canal](#1-estados-do-canal)
2. [Procedimentos do dia a dia](#2-procedimentos-do-dia-a-dia)
3. [Alertas e resposta](#3-alertas-e-resposta)
4. [Diagnóstico](#4-diagnóstico)
5. [DVR e armazenamento](#5-dvr-e-armazenamento)
6. [Manutenção de nós](#6-manutenção-de-nós)
7. [Testes de validação](#7-testes-de-validação)
8. [Registro de benchmarks](#8-registro-de-benchmarks)
9. [Registro de incidentes](#9-registro-de-incidentes)

---

## 1. Estados do canal

| Estado | O que significa | Ação do operador |
| --- | --- | --- |
| `STOPPED` | Parado manualmente | Nenhuma |
| `STARTING` | Iniciando | Aguardar; se passar de 30 s, ver [4.2](#42-canal-não-inicia) |
| `RUNNING` | Fonte principal ativa | Nenhuma |
| `DEGRADED` | Problema em confirmação | Acompanhar; o sistema decide em segundos |
| `SWITCHING` | Trocando de fonte | Aguardar |
| `ON_BACKUP` | Em fonte alternativa | Verificar a fonte principal com o fornecedor |
| `RECOVERING` | Testando a volta da principal | Aguardar |
| `FAILED` | Sem fonte ou limite de reinícios | Ver [4.1](#41-canal-sem-sinal) |

Detalhes da máquina de estados em [architecture.md](architecture.md#6-estados-do-canal-e-failover).

## 2. Procedimentos do dia a dia

### 2.1 Cadastrar um canal

1. Analisar a fonte antes de cadastrar: `POST /api/v1/sources/probe` ou `ffprobe` (ver [4.4](#44-comandos-úteis)).
2. Definir o modo: `auto` na maioria dos casos; `transcode` quando a fonte não servir para os clientes.
3. Cadastrar pelo painel ou por `POST /api/v1/channels`, com pelo menos uma fonte alternativa sempre que possível.
4. Conferir se o canal chegou a `RUNNING` e testar a reprodução em um player.

### 2.2 Trocar a fonte manualmente

Usar quando a fonte principal estiver no ar, mas com problema que o sistema não detecta (por exemplo, imagem congelada ou programação errada).

1. Painel → Canal → **Ativar esta fonte**, ou `POST /api/v1/channels/{id}/switch-source` com `{"priority": 2, "reason": "..."}`.
2. Registrar o motivo no campo `reason` (vai para a auditoria).
3. Ao escolher uma fonte alternativa, o retorno automático à principal fica suspenso até nova troca manual ou reinício do canal. Para voltar, ative a fonte principal manualmente.

### 2.3 Reiniciar um canal

Painel → Canal → **Reiniciar**, ou `POST /api/v1/channels/{id}/restart`. Reinícios manuais também contam no histórico do canal.

### 2.4 Rotina diária sugerida

- [ ] Ver canais em `FAILED` e `ON_BACKUP`.
- [ ] Ver os canais com mais reinícios e erros de continuidade nas últimas 24 h.
- [ ] Conferir o uso de disco do DVR e a previsão de esgotamento.
- [ ] Conferir se todos os workers estão `ONLINE`.
- [ ] Revisar alertas abertos.

## 3. Alertas e resposta

| Alerta | Condição | Severidade | Primeira resposta |
| --- | --- | --- | --- |
| Canal sem sinal | Sem pacotes há mais de 10 s | Crítica | [4.1](#41-canal-sem-sinal) |
| Canal em fonte alternativa | `ON_BACKUP` há mais de 10 min | Aviso | Verificar a fonte principal com o fornecedor |
| Reinícios excessivos | Mais de 5 em 10 min | Crítica | [4.3](#43-reinícios-frequentes) |
| Erros de continuidade | Taxa acima do limite definido | Aviso | [4.5](#45-erros-de-continuidade-cc) |
| Worker indisponível | Sem heartbeat há mais de 15 s | Crítica | [6.3](#63-nó-indisponível) |
| Disco do DVR | Uso acima de 85% | Aviso | [5.2](#52-disco-do-dvr-cheio) |
| Recursos do nó | CPU ou GPU acima de 90% por 5 min | Aviso | Ver canais com transcodificação no nó; drenar parte da carga |

Exemplo de regra Prometheus:

```yaml
groups:
  - name: nexostream
    rules:
      - alert: CanalSemSinal
        expr: time() - nexostream_channel_last_packet_timestamp_seconds > 10
        for: 0m
        labels:
          severity: critical
        annotations:
          summary: "Canal {{ $labels.channel_id }} sem pacotes há mais de 10 s"

      - alert: ReiniciosExcessivos
        expr: increase(nexostream_pipeline_restarts_total[10m]) > 5
        labels:
          severity: critical
        annotations:
          summary: "Canal {{ $labels.channel_id }} reiniciou mais de 5 vezes em 10 min"

      - alert: DiscoDVR
        expr: nexostream_dvr_storage_used_bytes / nexostream_dvr_storage_total_bytes > 0.85
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Volume {{ $labels.volume }} do DVR acima de 85%"
```

> A métrica `nexostream_dvr_storage_total_bytes` complementa a lista da especificação e precisa ser exposta pelo agente.

## 4. Diagnóstico

### 4.1 Canal sem sinal

1. Ver no status qual fonte está ativa e quando chegou o último pacote.
2. No nó do canal, verificar se o multicast chega à interface:

   ```bash
   sudo tcpdump -i eno2 -c 20 host 239.1.1.1 and udp port 1234
   ```

3. Se não chegar nada, verificar a adesão ao grupo e a rota:

   ```bash
   ip maddr show dev eno2
   cat /proc/net/igmp
   sysctl net.ipv4.conf.all.rp_filter net.ipv4.conf.eno2.rp_filter
   ```

4. Se os pacotes chegam, mas o canal não sai do estado de falha, analisar o fluxo com `ffprobe` e TSDuck ([4.4](#44-comandos-úteis)).
5. Confirmado o problema na origem, acionar o fornecedor e registrar o incidente.

### 4.2 Canal não inicia

- Conferir os logs do canal no Loki (`{channel_id="canal-001"}`) ou no nó:

  ```bash
  journalctl -u nexostream-worker --since "15 min ago" | grep canal-001
  ```

- Erros comuns: URL ou interface errada, codec não suportado pelo perfil, falta de recursos no nó, diretório sem permissão de escrita.

### 4.3 Reinícios frequentes

- Ver a mensagem de erro do pipeline no último reinício (evento `channel.pipeline_restarted`).
- Comparar com mudanças na fonte (`channel.input_changed`): troca de resolução ou de codec no meio da transmissão é causa frequente.
- Verificar memória do processo ao longo do tempo (possível vazamento).
- Se o nó estiver sobrecarregado, mover o canal para outro nó.

### 4.4 Comandos úteis

```bash
# Identificar codecs, resolução, FPS e bitrate
ffprobe -v error -show_format -show_streams -of json \
  "udp://239.1.1.1:1234?overrun_nonfatal=1&fifo_size=1000000"

# Ver o fluxo por alguns segundos e descartar (testa se decodifica sem erro)
ffmpeg -v warning -i "udp://239.1.1.1:1234?overrun_nonfatal=1&fifo_size=1000000" \
  -t 30 -f null -

# Continuidade e bitrate do MPEG-TS
tsp -I ip 239.1.1.1:1234 -P continuity -P bitrate_monitor --min 1000000 -O drop

# Resumo de PIDs e serviços do TS
tsp -I ip 239.1.1.1:1234 -P until --seconds 10 -P analyze -O drop

# Conferir a playlist HLS publicada
curl -s http://localhost/canal-001/index.m3u8

# Perda de pacotes UDP no nó (aumento de RcvbufErrors indica buffer insuficiente)
netstat -su | grep -i -E "receive|buffer"
```

### 4.5 Erros de continuidade (CC)

- Erros apenas em um canal costumam vir da origem.
- Erros em vários canais do mesmo nó ao mesmo tempo indicam problema local: buffer UDP insuficiente, interface saturada ou CPU sobrecarregada.
- Verificar `RcvbufErrors` ([4.4](#44-comandos-úteis)) e os ajustes de [deployment.md](deployment.md#42-ajustes-de-kernel-para-ingestão-udp).

### 4.6 Problemas de reprodução no cliente

| Sintoma | Causa provável | Verificação |
| --- | --- | --- |
| Player não abre | Playlist inexistente ou CORS | `curl` na playlist; cabeçalhos do nginx |
| Travamentos periódicos | Segmentos irregulares ou atrasados | Duração dos segmentos na playlist; GOP da fonte |
| Sem áudio | Codec de áudio não suportado pelo cliente (MP2, AC-3) | `ffprobe`; converter o áudio para AAC |
| Atraso alto | Janela HLS grande | Duração do segmento e tamanho da playlist |

## 5. DVR e armazenamento

### 5.1 Consultar gravações

`GET /api/v1/dvr/{channel_id}/recordings?from=&to=` mostra os intervalos gravados. Lacunas indicam períodos sem sinal ou sem gravação.

### 5.2 Disco do DVR cheio

1. Confirmar que a limpeza por retenção está rodando (último evento de limpeza).
2. Identificar os canais que mais ocupam espaço:

   ```bash
   du -sh /srv/nexostream/dvr/* | sort -h | tail -20
   ```

3. Reduzir temporariamente a retenção dos grupos menos críticos.
4. Planejar a ampliação, usando a fórmula da especificação: `GB por dia = canais × Mbps × 10,8`.

## 6. Manutenção de nós

### 6.1 Retirar um nó para manutenção

1. `POST /api/v1/workers/{id}/drain`.
2. Aguardar até o nó ficar sem canais.
3. Fazer a manutenção.
4. `POST /api/v1/workers/{id}/undrain`.

### 6.2 Atualizar o software

Seguir [deployment.md – Atualização dos nós](deployment.md#11-atualização-dos-nós), um nó por vez.

### 6.3 Nó indisponível

1. Confirmar se os canais do nó foram realocados (evento `worker.channels_reassigned`).
2. Se algum canal não foi realocado, verificar se existe outro nó com acesso à rede de ingestão da fonte e com recursos livres.
3. Investigar o nó: energia, rede de gerenciamento, `systemctl status nexostream-worker`, `journalctl -k` para erros de hardware.
4. Só retirar do modo drain depois de identificar a causa.

## 7. Testes de validação

### 7.1 Funcionais

- [ ] Ingestão UDP/MPEG-TS.
- [ ] Identificação de áudio e vídeo.
- [ ] Passthrough/remux.
- [ ] Transcodificação H.264/AAC.
- [ ] Saída HLS e MPEG-TS.
- [ ] Cadastro e edição de canais pela API.
- [ ] Exibição de métricas no painel.
- [ ] Gravação e reprodução do DVR.
- [ ] Failover entre fontes.

### 7.2 Estabilidade

- [ ] Teste contínuo de 72 horas.
- [ ] Teste contínuo de 7 dias.
- [ ] Reinício controlado de workers.
- [ ] Interrupção e retorno de fontes.
- [ ] Falha de rede.
- [ ] Falha de armazenamento (em ambiente de teste).
- [ ] Recuperação após reinicialização do servidor.

### 7.3 Recuperação: roteiros sugeridos

| Teste | Como provocar | Resultado esperado |
| --- | --- | --- |
| Queda da fonte principal | Parar o envio da fonte ou bloquear com `nftables` | `DEGRADED` → `SWITCHING` → `ON_BACKUP` dentro dos tempos configurados |
| Retorno da fonte principal | Restaurar o envio | `RECOVERING` → `RUNNING` após o tempo de estabilidade |
| Pipeline encerrado | `kill -9` no processo do canal | Reinício automático com espera exponencial |
| Worker encerrado | `systemctl stop nexostream-worker` | Canais realocados em até ~15 s |
| Nó desligado | Desligar o servidor | Canais realocados; nó `OFFLINE` no painel |
| Control Plane fora | Parar API e controller | Canais continuam no ar; failover local continua |

### 7.4 Escala

Subir a carga por etapas e só avançar quando a etapa anterior estiver estável:

1. 10 canais.
2. 50 canais.
3. 100 canais.
4. 250 canais.
5. 500 canais.
6. 1.000 canais.

Testar separadamente: somente passthrough, somente transcodificação, com DVR ativo e carga de espectadores.

## 8. Registro de benchmarks

Todo benchmark deve ser registrado em `tests/load/resultados/` com o modelo abaixo. Sem esse registro, o resultado não vale para o dimensionamento.

```markdown
# Benchmark AAAA-MM-DD — <cenário>

## Ambiente
- Servidor: <modelo, CPU, RAM, GPU, NICs, discos>
- SO e kernel: <versão>
- Versões: NexoStream <x.y.z>, GStreamer <x.y>, FFmpeg <x.y>, driver GPU <x.y>

## Cenário
- Canais: <quantidade>
- Modo: passthrough | transcodificação (<perfis>) | DVR
- Fontes: <codec, resolução, FPS, bitrate médio>
- Duração: <horas>

## Resultados
| Métrica | Média | Pico |
| --- | --- | --- |
| CPU (%) | | |
| Memória (GB) | | |
| GPU (%) | | |
| Rede de entrada (Gbps) | | |
| Rede de saída (Gbps) | | |
| Perda de pacotes (%) | | |
| Erros de continuidade | | |
| Reinícios de pipeline | | |
| Latência ponta a ponta (s) | | |

## Conclusão
<aprovado / reprovado, gargalo encontrado, próximos ajustes>
```

## 9. Registro de incidentes

Para cada incidente com impacto para os espectadores, registrar:

| Campo | Conteúdo |
| --- | --- |
| Início e fim | Horário em UTC |
| Canais e nós afetados | Lista |
| Impacto | O que os espectadores perceberam |
| Detecção | Alerta, painel ou reclamação |
| Causa | Raiz identificada |
| Ações tomadas | Em ordem cronológica |
| Ações preventivas | O que muda para não se repetir, com responsável |
