# Erros e transporte

Usar `gateway.IsCode(err, gateway.RateLimited)`, `errors.As` para `*gateway.Error` e `errors.Is` para cancelamento, deadline e falhas de callback. A fachada identifica o deployment em `Error.Provider`; os adaptadores diretos usam `openai_compatible`, `anthropic`, `gauss` ou `samsung`.

Erros HTTP preservam status e um `X-Request-Id` sanitizado, sem copiar corpos remotos para mensagens. HTTP 404 genérico permanece `upstream_error`: pode indicar rota inexistente, portanto não é assumido como modelo inexistente. `Retryable` é informação para o consumidor; nenhuma repetição é executada automaticamente.

O cliente padrão tem dial/cabeçalhos de até 30 segundos, timeout de operações comuns de 1 minuto e teto de streaming de 30 minutos; prazos menores do contexto prevalecem. `RequestTimeout` e `StreamTimeout` podem ser ajustados. Respostas têm limite padrão de 8 MiB, incluindo bytes do stream completo; eventos/linhas SSE, 1 MiB. `MaxResponseBytes` e `MaxEventBytes` ajustam esses limites.

TLS é validado por padrão. `InsecureTLS: true` vale somente para o transporte daquela instância e para todas as suas operações. Transportes HTTP padrão fornecidos pelo consumidor são clonados. Redirects são rejeitados; o transporte criado pela biblioteca não herda proxies de ambiente. Um `HTTPClient` customizado mantém seu timeout, proxy e outras políticas; para transportes que não sejam `*http.Transport`, a configuração TLS pertence ao consumidor, e `InsecureTLS` não é aceito.

As bases são configuração confiável da aplicação; esta entrega permite endereços locais e não implementa filtro de IP/DNS contra SSRF. Antes de aceitar URLs de usuários não confiáveis em um servidor, fornecer um transporte com política de rede apropriada. A biblioteca não persiste, registra ou imprime credenciais, prompts ou respostas por conta própria.


`Error.ModelAlias` e `Error.Model` identificam o alias e o modelo remoto resolvidos. `Error.Field` aponta para um campo a corrigir quando conhecido. Capacidade desconhecida, explicitamente negada e interface ausente têm mensagens diferentes. `CheckConfiguration(ctx)` agrega problemas locais em ordem de alias com `errors.Join`; não testa conectividade nem autenticação. `New` continua aceitando inicialização só para catálogo; a verificação acusa ausência de aliases para inferência.
