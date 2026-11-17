# 0010 — Controller redundante com eleição por lease

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

O controller decide em qual servidor cada canal roda, percebe servidores fora do ar e realoca os canais deles. Ele rodava numa instância só, o que fazia dele um ponto único de falha do Control Plane: se caísse, os canais continuavam no ar, porque o Data Plane é independente, mas nenhuma falha de servidor era tratada até ele voltar. A especificação lista a falha do plano de controle como risco alto (§20) e deixa a redundância como decisão pendente (§21).

## Decisão

- **Várias instâncias, um líder:** o `nexostream-controller` pode rodar em duas ou mais máquinas. Só o líder reconcilia, ou seja, atribui canais, envia comandos, marca servidores fora do ar, grava estatísticas e importa os guias de EPG.
- **Quem é o líder:** quem tem a lease `controller` na tabela `leases` do PostgreSQL.
  - O líder renova a lease a cada passe de reconciliação (5 s).
  - A lease vale por `NEXOSTREAM_LEADER_LEASE_TTL`, padrão 15 s.
  - Se o líder parar de renovar, por queda, travamento ou rede, outra instância pega a lease quando ela vence.
  - Num desligamento limpo, o líder libera a lease na hora.
- **Relógio:** a validade é calculada com o relógio do banco (`now()`), então controllers com relógios diferentes concordam sobre quem lidera.
- **Funções que todas as instâncias executam:** receber heartbeats e status e gravar eventos. Essas operações são idempotentes, então a instância em espera já está com o estado atualizado quando assume.
- **Identificação:** `NEXOSTREAM_CONTROLLER_ID`, mais o PID do processo. Os logs mostram "now the leader" e "on standby".

## Consequências

- **Mais fácil:**
  - manutenção de uma máquina de controle sem parar a reconciliação;
  - uma falha do controller vira uma pausa de no máximo 15 s na reação a falhas.
- **Mais difícil:**
  - **Dois líderes por pouco tempo:** um passe de reconciliação mais longo que a lease pode deixar dois líderes por um instante. Os comandos repetidos são idempotentes (o mesmo canal no mesmo servidor), mas isso precisa ser observado no laboratório.
  - **Banco é dependência:** o PostgreSQL já era um ponto único. Sem banco não há lease, e nenhum controller age, o que é o comportamento seguro. A redundância do banco (réplica com failover) fica fora deste ADR.
  - **Faltam outros componentes:** a redundância do NATS e do Redis segue a documentação de cada um e também está fora deste ADR.
- **Não comprovado:** o tempo de troca de líder com carga real.
