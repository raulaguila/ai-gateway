# Anthropic

Para construir o adaptador, importar `github.com/raulaguila/ai-gateway/providers/anthropic` e substituir a construção do adaptador por:

```go
provider, err := anthropic.New(anthropic.Config{
    APIKey: apiKey,
    Capabilities: gateway.Capabilities{Catalog: gateway.Supported},
    ModelCapabilities: map[string]gateway.Capabilities{
        model: {
            Chat: gateway.Supported,
            Streaming: gateway.Supported,
            Tools: gateway.Supported,
            Source: "configuration",
        },
    },
})
```

Depois de construir o adaptador, usar `gateway.NewSingle(provider, model)` ou configurar deployments e aliases no gateway completo. Declarar apenas as capacidades verificadas para o modelo escolhido. `BaseURL` é opcional e assume `https://api.anthropic.com`; aceita raiz, `/v1` final e prefixo de montagem, sem duplicar a versão em `/v1/messages` ou `/v1/models`.

O adaptador exige chave não vazia e envia `x-api-key` e `anthropic-version: 2023-06-01`. A versão é fixada conforme a especificação original deste projeto, preservando o perfil com `temperature` e `top_p`; não é uma seleção automática da versão mais recente. A documentação de [versionamento Anthropic](https://platform.claude.com/docs/en/api/versioning) registra a remoção desses parâmetros em versões mais novas. Restrições específicas de cada modelo continuam sendo aplicadas pelo servidor; não removemos parâmetros nem repetimos chamadas automaticamente.

O `max_tokens` obrigatório vem de `MaxOutputTokens` da requisição ou de `DefaultMaxOutputTokens` da configuração, com padrão 4096. Esse padrão não é orçamento de contexto nem limite validado contra o catálogo. `System` e mensagens `RoleSystem` iniciais são combinados no campo superior `system`; mensagens de sistema no meio da conversa são rejeitadas neste perfil, sem descarte silencioso. Conteúdo de usuário/assistente é convertido em blocos textuais, chamadas em `tool_use` e resultados consecutivos em blocos `tool_result` agrupados em mensagem `user`. Argumentos e IDs inválidos não são reparados silenciosamente.

Streaming exige `message_start`, blocos ordenados/fechados, motivo de término e `message_stop`. Um `message_delta` ou `[DONE]` não conclui a resposta. Texto e uso parciais permanecem disponíveis em erro, com `ProtocolComplete=false`. Eventos futuros desconhecidos e `ping` são ignorados; blocos de ferramentas em streaming retornam `unsupported_capability`. Blocos de raciocínio não viram texto visível. Thinking completo com replay de assinaturas, ferramentas do servidor e conteúdo multimodal ficam fora deste perfil textual.

O uso traduz `input_tokens` e `output_tokens`; os valores cumulativos de `message_delta` substituem os anteriores, sem somá-los. Campos específicos de cache não são expostos pelo contrato atual, portanto esses contadores não devem ser tratados como detalhamento de toda cobrança de cache. Motivos nativos são preservados, inclusive desconhecidos; `end_turn`/`stop_sequence`, `max_tokens`, `tool_use` e `refusal` têm mapeamento normalizado.

Catálogo percorre `has_more`/`last_id` com `after_id`, dentro de um único prazo. Limites: 10.000 modelos, 100 páginas e `MaxResponseBytes` para a soma das respostas. IDs duplicados, cursor sem avanço e refresh com erro não produzem catálogo parcial como sucesso. A listagem não modifica a configuração de capacidades. O request ID de erro é lido do cabeçalho `request-id` e sanitizado; HTTP 529 e `overloaded_error` viram `capacity_unavailable`.

`RequestTimeout`, `StreamTimeout`, `MaxResponseBytes`, `MaxEventBytes`, `HTTPClient` e `CloseIdleConnections` seguem a finalidade do adaptador compatível. Anthropic não oferece `InsecureTLS`; TLS permanece validado no transporte padrão, e uma CA privada pode ser fornecida por um cliente HTTP configurado pelo consumidor. O parser SSE e a criação isolada dos clientes são compartilhados internamente, sem mudança das interfaces públicas.


Exemplo executável: [examples/anthropic](../../examples/anthropic/main.go). A chave e o modelo vêm de `GATEWAY_API_KEY` e `GATEWAY_MODEL`; `GATEWAY_BASE_URL` é opcional. Aceita `-stream`.
