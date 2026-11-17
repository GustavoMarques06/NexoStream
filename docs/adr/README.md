# Registros de decisões de arquitetura (ADR)

Cada decisão de arquitetura relevante do NexoStream é registrada aqui, em um arquivo próprio, para que se saiba **o que** foi decidido, **por quê** e **quais as consequências**.

## Quando criar um ADR

- Escolha ou troca de tecnologia (engine de mídia, banco, mensageria, linguagem).
- Mudança na forma como os componentes se comunicam.
- Definição de limites, formatos ou contratos que outras partes vão usar.
- Qualquer decisão que alguém possa questionar daqui a seis meses.

## Como criar

1. Copiar o modelo abaixo para `NNNN-titulo-curto.md`, com o próximo número disponível.
2. Preencher com status `Proposta`.
3. Discutir no pull request.
4. Ao aprovar, mudar o status para `Aceita` e incluir na tabela de índice.
5. ADRs aceitos não são editados. Se a decisão mudar, criar um novo ADR que **substitui** o anterior e atualizar o status do antigo para `Substituída por NNNN`.

## Modelo

```markdown
# NNNN — Título da decisão

- **Status:** Proposta | Aceita | Rejeitada | Substituída por NNNN
- **Data:** AAAA-MM-DD
- **Responsáveis:** <nomes>

## Contexto
Qual problema ou necessidade levou a esta decisão. Restrições e forças envolvidas.

## Decisão
O que foi decidido, de forma direta.

## Alternativas consideradas
- Alternativa A: prós e contras.
- Alternativa B: prós e contras.

## Consequências
O que fica mais fácil, o que fica mais difícil, e o que precisa ser feito em seguida.

## Referências
Benchmarks, links e documentos relacionados.
```

## Índice

| Nº | Título | Status | Data |
| --- | --- | --- | --- |
| [0001](0001-separar-data-plane-e-control-plane.md) | Separar Data Plane e Control Plane | Aceita | 2026-11-16 |
| [0002](0002-integrar-engines-de-midia-existentes.md) | Integrar engines de mídia existentes em vez de criar um motor de codecs | Aceita | 2026-11-16 |
| [0003](0003-ffmpeg-como-engine-inicial.md) | FFmpeg como engine de mídia inicial, atrás de uma interface | Proposta | 2026-11-16 |
| [0004](0004-ingestao-e-analise-no-agente.md) | Ingestão UDP e análise MPEG-TS no próprio agente | Proposta | 2026-11-16 |
| [0005](0005-contratos-de-infraestrutura-e-modo-dev.md) | Contratos de infraestrutura com implementações em memória para desenvolvimento | Proposta | 2026-11-16 |
| [0006](0006-dvr-local-no-servidor-de-midia.md) | DVR local no servidor de mídia, no modelo do Flussonic | Proposta | 2026-11-16 |
| [0007](0007-configuracoes-gerenciadas-pelo-painel.md) | Configurações do sistema gerenciadas pelo painel | Proposta | 2026-11-16 |
| [0008](0008-drm-aes-128-com-token.md) | DRM: HLS AES-128 com token de reprodução | Proposta | 2026-11-16 |
| [0009](0009-sessoes-autorizacao-e-epg.md) | Sessões de reprodução, autorização e EPG por canal | Proposta | 2026-11-16 |
| [0010](0010-controller-redundante-com-lease.md) | Controller redundante com eleição por lease | Proposta | 2026-11-16 |
| [0011](0011-abr-com-um-decode.md) | ABR: várias qualidades com uma decodificação | Proposta | 2026-11-16 |
| [0012](0012-fronteiras-do-codigo-e-estado-compartilhado.md) | Fronteiras do código, estado compartilhado e versão do contrato | Proposta | 2026-11-17 |
| [0013](0013-autenticacao-reforcada.md) | Autenticação reforçada: renovação rotativa e bloqueio de conta | Proposta | 2026-11-17 |
