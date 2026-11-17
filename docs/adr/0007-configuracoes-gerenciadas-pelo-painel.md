# 0007 — Configurações do sistema gerenciadas pelo painel

- **Status:** Proposta
- **Data:** 2026-11-16
- **Responsáveis:** equipe NexoStream

## Contexto

Até aqui, parâmetros como a duração do segmento HLS, os padrões de failover, os custos do scheduler e a validade dos tokens vinham das variáveis de ambiente de cada processo. Para mudar um valor, era preciso editar o `.env` de cada servidor e reiniciá-lo. No Flussonic, o operador muda essas opções na interface ou no arquivo de configuração, e o servidor aplica a mudança sem reinício.

## Decisão

- **Onde ficam:** configurações de sistema e de cada servidor de mídia são gravadas no banco, como documentos JSON na tabela `settings`. A chave `global` guarda as do sistema e `server:<id>` as de cada servidor.
- **Onde se edita:**
  - na página **Configurações** do painel, que tem um menu lateral próprio:
    - **Sistema:** Geral, Entrada, Saídas HLS, Transcodificação.
    - **Infraestrutura:** DVR, Servidores, Cluster.
    - **Acesso:** Segurança, Usuários, API e tokens, Auditoria.
    - **Avançado:** Editor de texto.

    A aba DVR também reúne armazenamentos e RAID, discos e canais gravando. A aba Servidores mostra estado, carga e drain de cada nó junto com os ajustes dele. O menu principal do painel fica só com as telas de acompanhamento: Visão geral, Canais, Mosaico e Eventos;
  - no editor de texto, pelos blocos `settings { }` e `server <id> { }`.
- **Permissão:** exige o escopo `settings:admin`, que só o perfil `admin` tem. Toda alteração gera auditoria (`settings.update` e `settings.server_update`), com a lista de campos alterados.
- **Como chega aos servidores:**
  - A cada ciclo, o controller monta o runtime de cada nó (configurações globais de mídia mais os ajustes do nó). Se o hash do runtime for diferente do que o nó informa no heartbeat (`runtime_hash`), o controller envia o comando `configure`.
  - O nó grava o runtime em `StateDir/settings.json`. Assim, se reiniciar com o Control Plane fora do ar, volta com as mesmas configurações, e o Data Plane continua independente.
  - O nó só reinicia os pipelines quando muda algo que afeta a mídia: segmento ou janela HLS, perfil padrão, HEVC, buffer UDP ou interfaces. Mudanças de URL pública e de pasta do DVR valem sem reinício.
- **Precedência:** até o primeiro "salvar", cada processo usa o seu `.env`, e a página mostra esses valores. Depois do primeiro "salvar", valem os valores do painel. Num ajuste por servidor, campo vazio mantém o valor do `.env` daquele servidor.
- **Exceção:** `NEXOSTREAM_ALLOW_HEVC_PASSTHROUGH=true` no `.env` de um nó continua valendo mesmo que o painel desligue a opção.
- **O que fica no `.env`:** o que o processo precisa para subir: endereços do banco, do NATS e do Redis, a chave dos tokens, as portas, as pastas de trabalho e os caminhos do FFmpeg.
- **Marca (whitelabel):** nome, subtítulo, cor principal, logo e mensagem da tela de login também são configurações globais. O painel lê esses dados em `GET /branding`, uma rota pública, e os aplica no menu, no login, no título e no ícone da aba.
- **Canais novos:** recebem os padrões de failover, de retenção e de armazenamento do DVR. Canais existentes não mudam.

## Consequências

- **Mais fácil:**
  - mudar um parâmetro do cluster inteiro em um lugar;
  - ver quem mudou o quê;
  - exportar e versionar tudo em um único arquivo de texto.
- **Mais difícil:**
  - Durante alguns segundos após salvar, servidores diferentes podem estar com versões diferentes.
  - Uma mudança de HLS reinicia o empacotamento de todos os canais, o que causa alguns segundos de interrupção.

## Referências

- [api.md](../api.md), seção 14.4; [architecture.md](../architecture.md), seção 11.3.
