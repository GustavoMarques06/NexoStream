# 0005 — Contratos de infraestrutura com implementações em memória para desenvolvimento

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

A arquitetura usa PostgreSQL, Redis e NATS/JetStream. As estações de desenvolvimento nem sempre têm essa infraestrutura (ou Docker), e os testes de lógica de controle não deveriam depender dela.

## Decisão

- Cada dependência fica atrás de uma interface: `store.Store` (PostgreSQL), `statuscache.Cache` (Redis) e `bus.Bus` (NATS core para comandos, heartbeats e status; JetStream para eventos).
- Cada interface tem uma implementação em memória e uma suíte de conformidade comum (`storetest`, `bus.RunConformance`). As implementações reais precisam passar na mesma suíte: PostgreSQL no CI, NATS com servidor embarcado nos testes.
- O binário `nexostream-dev` junta API, controller e um worker em um processo, com as implementações em memória. Ele é só para desenvolvimento: os dados se perdem ao encerrar.
- Em produção, só os binários `nexostream-api`, `nexostream-controller` e `nexostream-worker` são usados, sempre com a infraestrutura real.

## Consequências

- **Mais fácil:** rodar e testar tudo em qualquer máquina; testes de integração rápidos (NATS embarcado, store em memória).
- **Mais difícil:** manter as duas implementações equivalentes. A suíte de conformidade é obrigatória para qualquer mudança no contrato.
- **Limite conhecido:** o modo dev não reproduz falhas de rede da infraestrutura real. Testes de recuperação de infraestrutura usam a stack real (`deploy/docker-compose.dev.yml`).
