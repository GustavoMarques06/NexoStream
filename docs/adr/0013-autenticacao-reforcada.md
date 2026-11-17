# 0013 — Autenticação reforçada: renovação rotativa e bloqueio de conta

- **Status:** Proposta
- **Data:** 2026-11-17
- **Responsáveis:** equipe NexoStream

## Contexto

O painel administra servidores de mídia inteiros: quem entra nele pode parar canais, ler gravações e criar tokens. A autenticação original tinha um limite de tentativas por IP e senhas com hash bcrypt, mas:

- o token de renovação durava 12 h e podia ser usado quantas vezes quisessem, então uma cópia roubada dava acesso até vencer, sem que ninguém percebesse;
- não havia bloqueio por conta, só por IP, e um ataque distribuído contra um usuário não era contido.

## Decisão

- **Renovação rotativa com detecção de reuso.** Cada token de renovação pertence a uma família (um login) e vale uma vez. Usá-lo gera o próximo da mesma família. Apresentar um token já usado indica cópia: a família inteira é revogada (inclusive o token mais novo do dono legítimo), responde-se `401 token_reused` e registra-se `auth.refresh_reuse`. Os tokens ficam na tabela `refresh_tokens`, com consumo atômico, então vale entre várias instâncias da API. Logout e troca de senha ou de perfil também os apagam.
- **Bloqueio progressivo por conta.** Depois de 5 falhas seguidas de senha, a conta bloqueia por 5 min, dobrando a cada múltiplo de 5, até 1 h. Tentativas durante o bloqueio não o estendem. A recusa é idêntica à de senha errada, para não revelar se a conta existe ou está bloqueada. Quem administra pode desbloquear.

## Consequências

- **Mais fácil:** perceber e conter um token de renovação roubado, e conter a adivinhação de senha contra uma conta específica.
- **Mais difícil:**
  - **Bloqueio como arma:** quem souber o nome de um usuário pode bloqueá-lo por uns minutos. É o custo de proteger contra adivinhação; o limite por IP e por usuário já reduz a velocidade, e o administrador pode desbloquear.
  - **Resposta perdida:** se a resposta de uma renovação se perder na rede, o cliente tem o token antigo, já usado. Isso é visto como reuso e exige novo login. Uma janela de tolerância enfraqueceria a detecção, então não há.
  - **Tokens antigos:** os tokens de renovação emitidos antes desta mudança não têm família e deixam de valer; os usuários entram de novo uma vez.
- **Não feito:** segundo fator de autenticação (foi implementado e depois retirado a pedido do responsável, e não faz parte desta decisão) e uma revisão de segurança externa.
