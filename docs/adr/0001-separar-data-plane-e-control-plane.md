# 0001 — Separar Data Plane e Control Plane

- **Status:** Aceita
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

O NexoStream precisa processar até 1.000 canais ao vivo com alta disponibilidade, ao mesmo tempo em que oferece API, painel, cadastro, agendamento de DVR e gestão de um cluster de servidores.

As duas cargas têm naturezas diferentes:

- O processamento de mídia é contínuo, sensível a latência e consome muita CPU, GPU, rede e disco.
- O gerenciamento é transacional, tem baixo volume e muda com mais frequência (novas telas, novas rotas, novas regras).

Se as duas cargas rodarem juntas, uma falha ou atualização do gerenciamento pode derrubar canais no ar, e um pico de mídia pode deixar o painel lento.

## Decisão

Dividir a plataforma em dois domínios independentes:

- **Data Plane:** pipelines de mídia e um agente local em cada Media Node, responsável por iniciar, supervisionar e fazer o failover dos canais daquele nó.
- **Control Plane:** API, painel, controller, scheduler, banco de dados e mensageria.

Regras decorrentes:

1. Os canais em execução continuam no ar se o Control Plane ficar indisponível.
2. O failover de fonte roda no agente local, sem depender do Control Plane.
3. A comunicação entre os domínios usa mensageria (NATS), nunca acesso direto ao banco pelos nós.
4. A entrega de mídia aos clientes não passa pela API.

## Alternativas consideradas

- **Aplicação única (monolito) com mídia e gerenciamento juntos:** mais simples no início, mas acopla a disponibilidade dos canais à do painel e dificulta escalar cada parte.
- **Nós totalmente autônomos, sem controle central:** reduz dependências, mas dificulta alocação de carga, realocação após falhas e visão única da operação.

## Consequências

- **Mais fácil:** atualizar API e painel sem interromper canais; escalar Media Nodes de forma independente; isolar falhas.
- **Mais difícil:** é preciso manter a consistência entre o estado desejado (banco) e o estado real (nós), o que exige um controller de reconciliação e eventos confiáveis.
- **Próximos passos:** definir o contrato de mensagens entre controller e agente, e o comportamento do agente quando perder a conexão com o NATS.

## Referências

- [Especificação do projeto](../../../NexoStream_Projeto.md), seções 3 e 23.
- [architecture.md](../architecture.md), seções 1, 4 e 9.
