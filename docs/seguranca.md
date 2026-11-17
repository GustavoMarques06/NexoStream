# Operação de segurança

Este guia é para quem opera o NexoStream em produção. Ele cobre os segredos, a rotação de chaves, a resposta a incidentes e o que a auditoria registra. Os detalhes da API estão em [api.md](api.md); as decisões, nos ADRs [0008](adr/0008-drm-aes-128-com-token.md), [0012](adr/0012-fronteiras-do-codigo-e-estado-compartilhado.md) e [0013](adr/0013-autenticacao-reforcada.md).

## 1. Segredos

Nenhum segredo vai no código, no repositório ou numa variável de ambiente em texto. Cada um fica num arquivo, indicado pela variável `<NOME>_FILE` (por exemplo, `NEXOSTREAM_JWT_KEY_FILE=/etc/nexostream/jwt.key`). Um teste (`tests/architecture`) falha se uma chave privada, um token ou uma senha em URL for commitado.

| Segredo | Variável | Onde | Como gerar |
| --- | --- | --- | --- |
| Assinatura das sessões do painel | `NEXOSTREAM_JWT_KEY_FILE` | API | `openssl rand -hex 32` (mínimo 32 bytes) |
| Tokens de reprodução | `NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE` | API e **todos** os servidores de mídia (o mesmo arquivo) | `openssl rand -hex 32` |
| Chaves do DRM | `NEXOSTREAM_DRM_KEY_FILE` | **Todos** os servidores de mídia (o mesmo arquivo) | `openssl rand -hex 32` |
| Senha do banco | `NEXOSTREAM_DATABASE_PASSWORD_FILE` | API e controller | gerenciador de senhas |
| Senha do Redis | `NEXOSTREAM_REDIS_PASSWORD_FILE` | API e controller | gerenciador de senhas |
| Credenciais do NATS | `NEXOSTREAM_NATS_CREDS` | todos | `nsc` |
| Administrador inicial | `NEXOSTREAM_BOOTSTRAP_ADMIN_PASSWORD_FILE` | API, só na primeira subida | **apague o arquivo** depois do primeiro login e troque a senha no painel |
| Certificados TLS | `NEXOSTREAM_TLS_CERT_FILE` e `NEXOSTREAM_TLS_KEY_FILE` | API e servidores de mídia | autoridade certificadora da operação |

**Permissões:** os arquivos ficam com dono `root`, grupo `nexostream` e modo `640`, como os `.env` em `/etc/nexostream/`.

## 2. Rotação de chaves

| Chave | O que acontece ao trocar | Como fazer |
| --- | --- | --- |
| JWT | Todas as sessões do painel caem; os usuários entram de novo. | Trocar o arquivo e reiniciar as instâncias da API. |
| Tokens de reprodução | Os links com `?token=` já entregues deixam de valer. | Trocar o arquivo na API e em **todos** os servidores de mídia, e reiniciar todos. Com arquivos diferentes, nenhum token funciona. |
| DRM | As chaves de conteúdo mudam. Um player que guardou a chave do período atual pode falhar até o período seguinte. | Fazer fora do horário de pico. Trocar em **todos** os servidores de mídia de uma vez. As gravações do DVR ficam em claro no disco e são cifradas na entrega, então continuam tocando. |
| Senhas de banco e Redis | — | Trocar no serviço e no arquivo, e reiniciar API e controller. |
| `passphrase` de um listener SRT | O encoder precisa da nova senha para conectar. | Trocar no canal e no encoder, ao mesmo tempo. |
| Chave de push (YouTube e outros) | — | Gerar uma nova no destino e trocar no canal. |

## 3. Resposta a incidentes

| Situação | O que fazer |
| --- | --- |
| **Token de integração vazado** | Revogar em Configurações › Tokens (`DELETE /api/v1/api-tokens/{id}`). O token para na hora. Criar outro com os escopos mínimos. |
| **Senha de usuário vazada** | Trocar a senha (`PATCH /api/v1/users/{id}` com `password`). Isso encerra todas as sessões e apaga os tokens de renovação do usuário. |
| **Conta suspeita** | Desativar (`{"disabled": true}`). A próxima requisição dela já é recusada. |
| **Auditoria mostra `auth.refresh_reuse`** | Alguém apresentou um token de renovação já usado: houve cópia. A sessão daquela família já foi revogada. Trocar a senha do usuário, verificar o IP no log e a máquina do usuário. |
| **Muitos `auth.account_locked`** | Alguém está tentando adivinhar senhas. Conferir os IPs em `auth.login_failed`, bloquear no firewall se for o caso. Para liberar um usuário legítimo: Configurações › Usuários › desbloquear (`{"unlock": true}`). |
| **Chave de push vazada** | Trocar no destino e no canal. Ela não aparece para quem só lê (é mascarada), então o vazamento veio de alguém com permissão de edição ou de fora do sistema. |
| **`passphrase` SRT vazada** | Trocar no canal e no encoder. Enquanto isso, quem tiver a senha antiga consegue enviar vídeo para o canal. |
| **Servidor de mídia comprometido** | Tirar do cluster (drain), trocar as chaves de reprodução e de DRM em todos, e as credenciais do NATS dele. |

## 4. Auditoria

A auditoria é append-only: o banco recusa apagar ou alterar registros. Lê-se em Configurações › Auditoria ou em `GET /api/v1/audit-log` (escopo `audit:read`).

| Ação | Quando |
| --- | --- |
| `auth.login` | Login com sucesso. |
| `auth.login_failed` | Senha errada, usuário inexistente ou desativado. |
| `auth.login_locked` | Tentativa numa conta bloqueada (não estende o bloqueio). |
| `auth.account_locked` | A conta foi bloqueada depois de falhas seguidas. |
| `auth.refresh_reuse` | Um token de renovação já usado reapareceu; a família foi revogada. |
| `auth.logout` | Saída explícita, que encerra todas as sessões do usuário. |
| `user.create`, `user.update` | Usuários criados ou alterados (senha, perfil, desativação, desbloqueio). |
| `api_token.create`, `api_token.revoke` | Tokens de integração. |
| `settings.update`, `settings.server_update` | Configurações do sistema e dos servidores. |
| `channel.create`, `channel.delete`, `config.apply` | Canais criados, apagados ou alterados pelo editor de texto. |
| `profile.*`, `dvr_storage.*`, `epg.refresh`, `session.kill` | Perfis, armazenamentos do DVR, guias e sessões encerradas por um operador. |

Endereços com senha ou chave aparecem mascarados (`••••`) na auditoria, nos eventos, no status e nos logs.

## 5. Exposição de rede

| Porta | Quem acessa | Proteção |
| --- | --- | --- |
| Painel e API (atrás do nginx) | Operadores | TLS no nginx ou na API; limite de login por IP e por usuário; bloqueio de conta. |
| Mídia do worker (HLS, DVR, MPEG-TS) | Espectadores | TLS com `NEXOSTREAM_TLS_CERT_FILE`/`KEY_FILE` ou nginx na frente; autorização por canal. |
| Métricas do worker (`NEXOSTREAM_METRICS_ADDR`) | Prometheus | HTTP sem autenticação: só na rede de gerência. Padrão: `127.0.0.1:9500`. |
| Listener SRT de um canal | Encoder | `passphrase` obrigatória (autentica e cifra). |
| PostgreSQL, Redis, NATS | Serviços internos | Rede interna, senha e TLS do NATS. |

O modo `nexostream-dev` não é para produção: ele não tem TLS, gera a senha do admin no terminal e escuta em todas as interfaces.

## 6. Antes de ir para produção

- [ ] Todos os segredos da seção 1 em arquivos `640`, nenhum em variável de ambiente em texto.
- [ ] Arquivo do administrador inicial apagado e senha trocada.
- [ ] TLS no painel e na entrega aos espectadores.
- [ ] Métricas do worker restritas à rede de gerência.
- [ ] `NEXOSTREAM_TRUSTED_PROXIES` só com os proxies reais, se houver algum além do nginx local.
- [ ] Usuários com o menor perfil necessário (o perfil "somente leitura" não vê senhas nem chaves).
- [ ] Tokens de integração com escopos mínimos e validade definida.
- [ ] Alerta no monitoramento para `auth.refresh_reuse` e para picos de `auth.account_locked`.
- [ ] CI verde: `go test ./...` (inclui o teste de segredos), `golangci-lint`, `govulncheck` e `npm audit`.
