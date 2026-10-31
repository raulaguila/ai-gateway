# Samsung Gauss

Importar `github.com/raulaguila/ai-gateway/providers/gauss`. Preferir `Config.Credentials` com campos `OpenAPIToken` e `ClientToken`. O campo legado `APIKey` continua aceitando JSON com `openapi_token` e `client_token`; não combinar os dois formatos. Ambos os tokens são obrigatórios; um prefixo `Bearer` existente no primeiro é normalizado. Eles seguem nos cabeçalhos `x-openapi-token` e `x-generative-ai-client`.

`BaseURL` assume o endpoint descrito no documento: `https://genai-openapi.sec.samsung.net/lahq/trial/api-llm`. Remove o sufixo `/openapi/llm/v1` quando presente e converte apenas o sufixo legado `/api-chat` para `/api-llm`. Catálogo e geração usam os caminhos `/openapi/llm/v1/models` e `/openapi/llm/v1/chat/completions`.

A inferência exige três identificadores:

| Identificador | Uso |
|---|---|
| `GUID` / `modelGuid` | Modelo selecionado na requisição e no alias |
| `RoutingID` / `modelId` | Inteiro positivo no cabeçalho `x-llm-model-id` |
| `ServingID` / `modelServingId` | Campo `model` do corpo remoto |

Exemplo de configuração e descoberta explícita, dentro de uma função que retorne `(gateway.ChatResult, error)`, usando `ctx`, `credentialJSON` e um `guid` selecionado pelo consumidor:

```go
provider, err := gauss.New(gauss.Config{APIKey: credentialJSON})
if err != nil {
    return gateway.ChatResult{}, err
}
defer provider.CloseIdleConnections()

if _, err := provider.ListModels(ctx); err != nil {
    return gateway.ChatResult{}, err
}
client, err := gateway.New(gateway.Config{
    Deployments: map[string]gateway.Provider{"corporate": provider},
    Models: map[string]gateway.ModelRoute{
        "chat": {Deployment: "corporate", Model: guid},
    },
})
if err != nil {
    return gateway.ChatResult{}, err
}
return client.Chat(ctx, gateway.ChatRequest{
    Model: "chat",
    Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Olá!"}},
})
```

Também é possível fornecer `Config.Models []gauss.Model` e `CatalogUpdatedAt` com um snapshot persistido pelo consumidor, sem rede durante a construção. `Snapshot()` devolve cópia dos modelos nativos e a data da última carga. O TTL padrão é 10 minutos, configurável por `CatalogTTL`. Snapshot ausente ou expirado falha antes da chamada de geração e pede `ListModels`; não há descoberta escondida. Refresh com erro mantém os dados e a data anteriores. Um catálogo vazio válido remove os modelos anteriores.

Os testes incluem agora a fixture de catálogo encontrada no projeto local Thoth, copiada para `providers/gauss/testdata/thoth-models.json`, com oito modelos. Ela vem de `backend/internal/adapter/outbound/modelcatalog/testdata/gauss-models.json`; nenhuma chamada remota foi feita. O parser aceita lista raiz ou envelopes aninhados `models`, `data`, `result` e `llmModels`, com profundidade limitada. Cada linha usa `modelGuid`, `modelId` e `modelServingId`; linhas inválidas são descartadas e GUIDs repetidos são deduplicados. Um retorno não vazio sem nenhuma linha válida é erro, preservando o snapshot anterior. Há limites de 10.000 modelos e 8 MiB por resposta por padrão; paginação indicada por `has_more` é rejeitada.

O snapshot nativo preserva nome, descrição e contexto. O formato verificado no Thoth usa listas de traduções `{languageCode, content}`; também aceitamos string ou objeto de idiomas. Inglês tem preferência, depois a primeira tradução preenchida. Linhas `Tool Calling: O/X`, inclusive com prefixo `-`, declaram suporte a ferramentas. Contexto vem de `contextWindow`, `context_window`, `contextLength`, `context_length`, `max_model_len` ou linha `Context Length/Window`, também com prefixo `-` e sufixo `tokens`; `64k` corresponde a 64000. Não inferimos capacidades pelo nome. Esses metadados ficam em `Snapshot()`, sem credenciais. A janela descoberta não configura a política automaticamente; usar seu valor explicitamente em `ModelRoute.TokenPolicy.ContextWindow` quando apropriado.

Chat e SSE reutilizam a implementação de Chat Completions, sem enviar `stream_options`. A camada Gauss filtra `<think>`, `<thinking>`, `<analysis>` e `<reasoning>` antes de qualquer callback, inclusive entre chunks e com blocos aninhados. Tags parciais e blocos ocultos sem fechamento são descartados conservadoramente; nomes de tags com atributos não fazem parte deste perfil. Resposta apenas oculta ou em branco é erro, salvo ferramentas válidas, limite de tokens ou filtro de conteúdo. O resultado final e o evento `completed` usam somente o texto visível. Ferramentas em streaming permanecem não suportadas.

Os testes do Thoth também usam SSE com um JSON completo por linha `data:`, sem linha em branco separadora e com frame final no EOF. Um leitor privado do Gauss normaliza esse formato antes do parser compartilhado e mantém suporte a JSON multilinha padrão. A interpretação do corpo é feita pelo protocolo solicitado, mesmo quando o servidor usa Content-Type genérico. Os limites de bytes e linha continuam sendo aplicados; o parser OpenAI genérico permanece estrito.

O limitador aplica **3 tentativas de geração por minuto**, em janela móvel por base normalizada e token OpenAPI, compartilhada entre todas as instâncias no mesmo processo. O segundo token não cria outra cota. Essa política vem do documento e não representa garantia contratual do fornecedor. Catálogo não consome uma vaga, mas respeita cooldown. Entradas ociosas do registro são limpas sem manter credenciais no limitador; seu identificador é um hash.

`FailIfBusy: true` retorna `capacity_unavailable` imediatamente quando uma geração precisaria esperar. No modo padrão, a espera é cancelável e integra o prazo da operação. `OnWait(ctx, duration)` permite acompanhar chat e catálogo; pode ser chamado concorrentemente por operações diferentes e pode interromper a espera retornando erro. Streaming também emite `EventProviderWait` com `Wait.Provider` e `Wait.Duration`; o gateway identifica o deployment nesse evento. Esperas podem ocorrer antes da admissão SSE. Falhas nessa fase retornam diretamente, sem evento terminal; após admissão, o contrato terminal é o mesmo dos demais adaptadores.

A exceção de retry do Gauss é **HTTP 429 antes de entregar o corpo da resposta**: observa `Retry-After` e/ou `nextAccessTime` em cabeçalho ou corpo, usando `Date` do servidor como referência para timestamps. `nextAccessTime` aceita RFC3339 ou epoch em segundos/milissegundos. Usa a maior orientação válida, com folga de 250 ms; na ausência de orientação, aguarda 1 minuto. Cooldown é compartilhado inclusive após esgotar tentativas. `MaxAttempts` tem padrão 3; definir 1 desativa repetição. Cada tentativa POST consome cota. Não há retry de erros de rede, 5xx ou de streams já iniciados.

O formato específico `2026-Oct-28 12:01:00+0000 UTC`, presente nos testes de rate limit do Thoth, também é aceito em `nextAccessTime`.

Timeout comum padrão: 2 minutos, incluindo espera; streaming: 30 minutos, ambos configuráveis. `MaxResponseBytes`, `MaxEventBytes` e `HTTPClient` seguem a finalidade dos demais adaptadores. TLS permanece validado; não há opção `InsecureTLS` no Gauss. Para mais de um processo/servidor compartilhando a credencial, o consumidor precisa de coordenação externa: este limitador não é distribuído.


## Inicialização simples

```go
provider, err := gauss.New(gauss.Config{
    Credentials: gauss.Credentials{OpenAPIToken: openAPIToken, ClientToken: clientToken},
})
if err != nil { return err }
defer provider.CloseIdleConnections()
if _, err := provider.RefreshModels(ctx); err != nil { return err }
client, err := gateway.NewSingle(provider, guid)
if err != nil { return err }
_ = client
```

`RefreshModels` é a operação explícita de atualização; `ListModels` mantém o mesmo comportamento para compatibilidade. `CatalogStatus()` inspeciona `Loaded`, `Stale`, quantidade de modelos, última atualização e expiração sem rede. Falha de refresh preserva o snapshot e seu estado. `NewSingle` e `CheckConfiguration` diagnosticam catálogo ausente/expirado ou GUID ausente antes da geração. Não há refresh automático ou retry adicional.

Exemplo executável: [examples/gauss](../../examples/gauss/main.go), usando `GAUSS_OPENAPI_TOKEN`, `GAUSS_CLIENT_TOKEN` e `GATEWAY_MODEL` (GUID). A base é opcional; aceita `-stream`.
