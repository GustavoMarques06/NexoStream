# Implantação

Guia de implantação do NexoStream em servidores Linux bare metal.

> **Status:** os serviços `nexostream-api`, `nexostream-controller` e `nexostream-worker` existem e os arquivos de implantação estão versionados em [`deploy/`](../deploy): units systemd, `.env` de exemplo com todas as variáveis, nginx, sysctl, NATS, Prometheus, Grafana, Promtail e `deploy/scripts/install.sh`. Ainda não foram validados em um servidor de laboratório. Valores de sistema e rede são pontos de partida e devem ser ajustados nos testes de carga.
>
> Diferenças em relação ao texto abaixo: o worker também escuta em `127.0.0.1:9501` (`NEXOSTREAM_MEDIA_ADDR`, relay MPEG-TS/HTTP atrás do nginx); a origem nginx inclui o proxy de `/<canal>/stream.ts` ([`deploy/configs/nginx/nexostream-origin.conf`](../deploy/configs/nginx/nexostream-origin.conf)); o worker guarda a última configuração recebida em `/var/lib/nexostream/worker/channels.json`.

## Sumário

1. [Topologia](#1-topologia)
2. [Hardware de laboratório](#2-hardware-de-laboratório)
3. [Redes e portas](#3-redes-e-portas)
4. [Preparação do sistema](#4-preparação-do-sistema)
5. [Dependências](#5-dependências)
6. [Usuário, diretórios e configuração](#6-usuário-diretórios-e-configuração)
7. [Origem HTTP para HLS (nginx)](#7-origem-http-para-hls-nginx)
8. [Serviços systemd](#8-serviços-systemd)
9. [Observabilidade](#9-observabilidade)
10. [Segurança](#10-segurança)
11. [Atualização dos nós](#11-atualização-dos-nós)
12. [Checklist de implantação](#12-checklist-de-implantação)

---

## 1. Topologia

### 1.1 Laboratório (Fases 1 a 6)

Um único servidor com todos os componentes:

```text
+------------------------------------------------------+
| Servidor de laboratório                              |
|  nexostream-api  nexostream-controller               |
|  nexostream-worker + pipelines                       |
|  PostgreSQL  Redis  NATS  nginx                      |
|  Prometheus  Grafana  Loki  node_exporter            |
+------------------------------------------------------+
```

### 1.2 Cluster (Fase 7 em diante)

```text
          Rede de gerenciamento
 +-------------------------------------+
 | Nó de controle                      |
 |  API, controller, PostgreSQL,       |
 |  Redis, NATS, Prometheus, Grafana,  |
 |  Loki                               |
 +------------------+------------------+
                    |
     +--------------+--------------+
     |              |              |
 +---+----+    +----+---+     +----+---+
 |media-01|    |media-02| ... |media-N |   worker + pipelines + nginx + DVR
 +---+----+    +----+---+     +----+---+
     |              |              |
  Rede de ingestão (fontes)   Rede de distribuição (clientes)
```

A hipótese inicial de teste é de 4 Media Nodes com 250 canais cada. Não é uma capacidade garantida.

## 2. Hardware de laboratório

O servidor de laboratório deve representar o hardware pretendido para produção. Caso contrário, os benchmarks não servem para o dimensionamento.

| Item | Recomendação inicial |
| --- | --- |
| CPU | Mesma família e geração prevista para produção |
| GPU | A mesma prevista (NVIDIA para NVENC ou Intel com Quick Sync), se houver transcodificação |
| Memória | 64 GB ou mais |
| Rede | Interfaces separadas para ingestão, distribuição e gerenciamento; 10 Gbps ou mais para ingestão e distribuição |
| Disco do sistema | SSD/NVMe em RAID 1 |
| Disco de HLS ao vivo | NVMe (muitas gravações pequenas e contínuas) ou `tmpfs` para a janela ao vivo |
| Disco do DVR | Volume dedicado, com throughput medido por `fio` antes do uso |

## 3. Redes e portas

### 3.1 Separação de redes

| Rede | Uso |
| --- | --- |
| Ingestão | Recebimento das fontes (multicast e unicast) |
| Distribuição | Entrega HLS/TS aos clientes |
| Gerenciamento | API, painel, NATS, PostgreSQL, Redis, métricas, logs e SSH |
| Armazenamento | Tráfego para o DVR, quando o storage for externo |

### 3.2 Portas [Proposta]

| Serviço | Porta | Rede | Exposição |
| --- | --- | --- | --- |
| nginx (painel e API via proxy) | 443/tcp | Gerenciamento | Operadores |
| nexostream-api | 8080/tcp | localhost | Somente via nginx |
| nginx (origem HLS/TS) | 80/tcp, 443/tcp | Distribuição | Clientes ou CDN |
| nexostream-worker (`/metrics`) | 9500/tcp | Gerenciamento | Prometheus |
| nexostream-worker (relay MPEG-TS/HTTP) | 9501/tcp | localhost | Somente via nginx de origem |
| NATS | 4222/tcp | Gerenciamento | Nós do cluster |
| PostgreSQL | 5432/tcp | Gerenciamento | API e controller |
| Redis | 6379/tcp | Gerenciamento | API e controller |
| Prometheus | 9090/tcp | Gerenciamento | Grafana |
| Grafana | 3000/tcp | Gerenciamento | Operadores |
| Loki | 3100/tcp | Gerenciamento | Agentes de log |
| node_exporter | 9100/tcp | Gerenciamento | Prometheus |
| DCGM Exporter (GPU NVIDIA) | 9400/tcp | Gerenciamento | Prometheus |
| Fontes UDP | conforme cadastro | Ingestão | Somente origem das fontes |

## 4. Preparação do sistema

### 4.1 Sistema operacional

- Ubuntu Server LTS, instalação mínima.
- Fuso horário UTC e sincronização de horário com `chrony`. O DVR e o failover dependem de horário correto.
- Atualizações automáticas de segurança ativadas, com reinício programado fora do horário de pico.

```bash
sudo timedatectl set-timezone UTC
sudo apt install -y chrony
```

### 4.2 Ajustes de kernel para ingestão UDP

```ini
# /etc/sysctl.d/90-nexostream.conf
net.core.rmem_max = 67108864
net.core.rmem_default = 4194304
net.core.wmem_max = 67108864
net.core.netdev_max_backlog = 250000
# Necessário em alguns cenários de multicast com várias interfaces
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
# Muitos grupos multicast por nó
net.ipv4.igmp_max_memberships = 1024
fs.file-max = 2097152
```

```bash
sudo sysctl --system
```

### 4.3 Placas de rede e CPU

```bash
# Aumentar o ring buffer de recepção (verificar o máximo com: ethtool -g eno2)
sudo ethtool -G eno2 rx 4096

# Governador de CPU em desempenho máximo
sudo apt install -y linux-tools-common linux-tools-$(uname -r)
sudo cpupower frequency-set -g performance
```

Outros pontos a verificar no laboratório:

- Distribuição de interrupções entre núcleos (`irqbalance` ou afinidade manual).
- IGMP habilitado nos switches (IGMP snooping e querier na VLAN de ingestão).
- MTU consistente em toda a rede de ingestão.

Esses ajustes precisam persistir após o reinício (por exemplo, via `netplan`, `udev` ou um serviço systemd de configuração).

## 5. Dependências

### 5.1 Media Nodes

```bash
sudo apt install -y ffmpeg nginx \
  gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
  gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav \
  prometheus-node-exporter ethtool
```

- **NVIDIA:** driver oficial e suporte a NVENC no FFmpeg/GStreamer. Verificar com `ffmpeg -hide_banner -encoders | grep nvenc`.
- **Intel Quick Sync:** `intel-media-va-driver-non-free` e `vainfo`. Verificar com `ffmpeg -hide_banner -encoders | grep qsv`.
- **TSDuck** (opcional, para diagnóstico de MPEG-TS): instalar pelo pacote `.deb` oficial do projeto.

### 5.2 Nó de controle

| Componente | Observação |
| --- | --- |
| PostgreSQL | Versão suportada mais recente do repositório oficial do PostgreSQL |
| Redis | Com persistência AOF desativada ou moderada (estado é reconstruível) |
| NATS Server | Com JetStream habilitado e diretório de dados dedicado |
| Prometheus, Grafana, Loki | Pacotes oficiais de cada projeto |

## 6. Usuário, diretórios e configuração

```bash
sudo useradd --system --home /var/lib/nexostream --shell /usr/sbin/nologin nexostream
sudo mkdir -p /etc/nexostream /var/lib/nexostream/hls /srv/nexostream/dvr /var/log/nexostream
sudo chown -R nexostream:nexostream /var/lib/nexostream /srv/nexostream /var/log/nexostream
sudo chmod 750 /etc/nexostream
```

| Caminho | Conteúdo |
| --- | --- |
| `/usr/local/bin/nexostream-*` | Binários |
| `/etc/nexostream/*.env` | Configuração e segredos (permissão `640`, dono `root:nexostream`) |
| `/var/lib/nexostream/hls/` | Janela ao vivo de HLS |
| `/srv/nexostream/dvr/` | Gravações do DVR |
| `/var/log/nexostream/` | Logs locais, se o envio ao Loki falhar |

Exemplo de configuração do worker:

```ini
# /etc/nexostream/worker.env
NEXOSTREAM_WORKER_ID=media-01
NEXOSTREAM_NATS_URL=tls://10.10.0.10:4222
NEXOSTREAM_NATS_CREDS=/etc/nexostream/worker.creds
NEXOSTREAM_HLS_DIR=/var/lib/nexostream/hls
NEXOSTREAM_DVR_DIR=/srv/nexostream/dvr
NEXOSTREAM_METRICS_ADDR=10.10.0.21:9500
NEXOSTREAM_INGEST_INTERFACES=eno2,eno3
NEXOSTREAM_MAX_CPU_PERCENT=80
NEXOSTREAM_LOG_LEVEL=info
```

Exemplo de configuração da API:

```ini
# /etc/nexostream/api.env
NEXOSTREAM_HTTP_ADDR=127.0.0.1:8080
NEXOSTREAM_DATABASE_URL=postgres://nexostream@10.10.0.10:5432/nexostream?sslmode=verify-full
NEXOSTREAM_DATABASE_PASSWORD_FILE=/etc/nexostream/db.password
NEXOSTREAM_REDIS_URL=rediss://10.10.0.10:6379/0
NEXOSTREAM_NATS_URL=tls://10.10.0.10:4222
NEXOSTREAM_JWT_KEY_FILE=/etc/nexostream/jwt.key
NEXOSTREAM_LOG_LEVEL=info
```

## 7. Origem HTTP para HLS (nginx)

```nginx
# /etc/nginx/sites-available/nexostream-origin
server {
    listen 80;
    server_name _;

    root /var/lib/nexostream/hls;

    location ~ \.m3u8$ {
        types { application/vnd.apple.mpegurl m3u8; }
        add_header Cache-Control "no-cache";
        add_header Access-Control-Allow-Origin "*";
    }

    location ~ \.ts$ {
        types { video/mp2t ts; }
        add_header Cache-Control "max-age=60";
        add_header Access-Control-Allow-Origin "*";
    }

    location / {
        return 404;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/nexostream-origin /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

Desde o [ADR 0009](adr/0009-sessoes-autorizacao-e-epg.md), o nginx não serve mais os arquivos HLS direto: ele repassa `/<canal>/…` ao worker, que conta as sessões, aplica a autorização do canal e cifra os canais com DRM. O nginx manda o IP do espectador em `X-Real-IP`.

A configuração completa, com o relay MPEG-TS, o DVR e os canais com DRM, está em [`deploy/configs/nginx/nexostream-origin.conf`](../deploy/configs/nginx/nexostream-origin.conf). Para canais com DRM ([ADR 0008](adr/0008-drm-aes-128-com-token.md)), ela acrescenta:

- **`/<canal>/enc/`:** segmentos cifrados pelo worker.
- **`/<canal>/keys/`:** chaves, nunca guardadas em cache.

Os segredos do DRM são dois arquivos, cada um com pelo menos 32 bytes aleatórios:

```bash
sudo sh -c 'openssl rand -hex 32 > /etc/nexostream/drm.key; openssl rand -hex 32 > /etc/nexostream/playback-token.key'
sudo chown root:nexostream /etc/nexostream/drm.key /etc/nexostream/playback-token.key
sudo chmod 640 /etc/nexostream/drm.key /etc/nexostream/playback-token.key
```

- **`NEXOSTREAM_DRM_KEY_FILE`:** só nos Media Nodes, e o mesmo arquivo em todos eles.
- **`NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE`:** nos Media Nodes e na API. O middleware de assinantes também precisa dele se for assinar os próprios tokens.

Os arquivos são copiados por canal seguro, nunca pelo repositório.

- As playlists não devem ser armazenadas em cache, porque mudam a cada segmento.
- Os segmentos podem ser armazenados em cache por pouco tempo, já que não mudam depois de escritos.
- Em produção, usar HTTPS e restringir o `Access-Control-Allow-Origin` aos domínios dos players.

## 8. Serviços systemd

### 8.1 Worker

```ini
# /etc/systemd/system/nexostream-worker.service
[Unit]
Description=NexoStream Media Worker
After=network-online.target
Wants=network-online.target

[Service]
User=nexostream
Group=nexostream
EnvironmentFile=/etc/nexostream/worker.env
ExecStart=/usr/local/bin/nexostream-worker
Restart=always
RestartSec=2
LimitNOFILE=1048576
# Parar o agente sem matar os pipelines na hora (o agente encerra cada canal de forma ordenada)
KillMode=mixed
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
```

### 8.2 API e controller

```ini
# /etc/systemd/system/nexostream-api.service
[Unit]
Description=NexoStream API
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=nexostream
Group=nexostream
EnvironmentFile=/etc/nexostream/api.env
ExecStart=/usr/local/bin/nexostream-api
Restart=always
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

`nexostream-controller.service` segue o mesmo modelo, com `EnvironmentFile=/etc/nexostream/controller.env`.

**Redundância do controller** ([ADR 0010](adr/0010-controller-redundante-com-lease.md)):

- **Montagem:** instale o `nexostream-controller` em duas máquinas de controle, com o mesmo banco, o mesmo NATS e o mesmo Redis, e um `NEXOSTREAM_CONTROLLER_ID` diferente em cada uma.
- **Funcionamento:** só uma reconcilia por vez, a dona da lease `controller` no PostgreSQL. A outra fica em espera e assume em até `NEXOSTREAM_LEADER_LEASE_TTL` (padrão 15 s) se a primeira cair.
- **Como verificar:** `journalctl -u nexostream-controller | grep -E "now the leader|on standby"`.
- **API:** pode ter várias instâncias atrás do nginx, sem eleição, porque é sem estado.

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now nexostream-api nexostream-controller nexostream-worker
```

## 9. Observabilidade

```yaml
# Trecho de /etc/prometheus/prometheus.yml
scrape_configs:
  - job_name: nexostream-worker
    static_configs:
      - targets: ["10.10.0.21:9500", "10.10.0.22:9500"]
  - job_name: node
    static_configs:
      - targets: ["10.10.0.21:9100", "10.10.0.22:9100"]
  - job_name: nvidia-gpu
    static_configs:
      - targets: ["10.10.0.21:9400", "10.10.0.22:9400"]
```

- Dashboards e regras de alerta ficam versionados em `deploy/configs/grafana/` e `deploy/configs/prometheus/`.
- Os alertas iniciais estão em [operations.md](operations.md#3-alertas-e-resposta).

## 10. Segurança

- TLS em todas as interfaces expostas (painel, API, origem HLS em produção, NATS, PostgreSQL, Redis).
- Firewall (`nftables` ou `ufw`) liberando apenas as portas da [seção 3.2](#32-portas-proposta), cada uma somente na sua rede.
- SSH apenas pela rede de gerenciamento, com chave e sem login de `root`.
- Segredos em arquivos com permissão restrita ou em cofre; nunca no repositório. Inclui `drm.key` e `playback-token.key` (seção 7).
- Cada serviço com seu próprio usuário de banco e permissões mínimas.
- Credenciais NATS por nó, para revogar um nó sem afetar os demais.

## 11. Atualização dos nós

1. Colocar o nó em modo drain (`POST /api/v1/workers/{id}/drain`).
2. Aguardar a migração dos canais (painel ou `GET /api/v1/workers/{id}`).
3. Atualizar binários e pacotes.
4. Reiniciar os serviços e conferir o heartbeat.
5. Retirar o nó do modo drain (`POST /api/v1/workers/{id}/undrain`).
6. Repetir um nó por vez.

A instalação e a atualização devem ser automatizadas (Ansible ou ferramenta equivalente) a partir da Fase 7.

## 12. Checklist de implantação

- [ ] Horário sincronizado (`chronyc tracking`).
- [ ] `sysctl` aplicados e persistentes.
- [ ] Ring buffers e governador de CPU configurados.
- [ ] Multicast chegando em cada interface de ingestão (`ffprobe` em uma fonte de cada rede).
- [ ] Encoders de hardware detectados, quando houver GPU.
- [ ] Throughput do disco do DVR medido com `fio`.
- [ ] Banda entre nós medida com `iperf3`.
- [ ] nginx servindo `/canal-teste/index.m3u8`.
- [ ] Serviços `nexostream-*` ativos e reiniciando sozinhos após `kill`.
- [ ] Métricas aparecendo no Prometheus e dashboards no Grafana.
- [ ] Firewall revisado e portas de gerenciamento fechadas para as redes externas.
