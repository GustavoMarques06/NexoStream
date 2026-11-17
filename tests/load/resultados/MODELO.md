# Benchmark AAAA-MM-DD — <cenário>

## Ambiente
- Servidor: <modelo, CPU, RAM, GPU, NICs, discos>
- SO e kernel: <versão>
- Versões: NexoStream <x.y.z (commit)>, FFmpeg <x.y>, driver GPU <x.y>
- Ajustes aplicados: <sysctl, ring buffers, governador de CPU, afinidade de IRQ>

## Cenário
- Canais: <quantidade>
- Modo: passthrough | transcodificação (<perfis>, encoder <cpu|nvenc|qsv>) | DVR
- Fontes: <codec, resolução, FPS, bitrate médio; sintéticas ou gravações reais>
- Origem das fontes: <máquina geradora, rede>
- Duração: <horas>

## Resultados
| Métrica | Média | Pico |
| --- | --- | --- |
| CPU (%) | | |
| Memória (GB) | | |
| GPU (%) | | |
| Rede de entrada (Gbps) | | |
| Rede de saída (Gbps) | | |
| Perda de pacotes (%) / RcvbufErrors | | |
| Erros de continuidade | | |
| Entrada descartada (bytes) | | |
| Reinícios de pipeline | | |
| Latência ponta a ponta (s) | | |

## Conclusão
<aprovado / reprovado, gargalo encontrado, próximos ajustes>

## Anexos
- CSV do `collect.sh`: `raw/<arquivo>.csv`
- Capturas do Grafana
