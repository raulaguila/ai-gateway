# OpenAI Compatible e Ollama

- `Chat`: POST `/chat/completions`, `stream=false`. Ferramentas são traduzidas como funções; o consumidor executa as chamadas e envia mensagens `RoleTool` com o `ToolCallID` correspondente na próxima rodada. Argumentos devem ser objetos JSON válidos. IDs retornados precisam ser únicos na resposta e são preservados no histórico; nenhuma ferramenta é executada pela biblioteca.
- `Stream`: POST `/chat/completions`, `stream=true`, retorno SSE. Eventos: `text_delta`, `usage`, `completed`, `error`. O callback é síncrono e recebe sequência a partir de 1. Falha do callback encerra a leitura sem novas notificações; sua causa é preservada para `errors.Is`. Não usar o callback para chamar a própria operação de forma bloqueante.
- `Embed`: POST `/embeddings`, com entrada em lista e `encoding_format=float`. Reordena por índice e rejeita duplicação, lote incompleto, vetor vazio, dimensão inconsistente, componentes nulos/não finitos e mudança explícita do modelo retornado. Usar o ID exato do modelo, incluindo tag quando aplicável, para evitar divergência com nomes canônicos retornados pelo servidor. O resultado não contém vetores parciais em erro.
- `ListModels`: GET `/models`, limitado a 10.000 modelos e ao limite de bytes. Catálogo vazio é válido; catálogo ausente, IDs inválidos/duplicados ou paginação indicada por `has_more` são erros. Paginação não é implementada nesta entrega.

`Temperature`, `TopP` e `MaxOutputTokens` são opcionais por ponteiro: zero explícito não vira ausência. `MaxTokensField` escolhe `max_tokens` (padrão) ou `max_completion_tokens` por deployment. `IncludeStreamUsage` é opt-in. Não há negociação por tentativa, remoção automática de parâmetros nem retentativas de geração.

O parser SSE monta eventos com múltiplas linhas `data:`. `[DONE]` encerra o stream; um `finish_reason` permite aceitar EOF válido, mas a leitura continua para receber uso posterior. EOF sem marcador de término falha com `stream_incomplete`, preservando texto e uso parciais. Erros após o término informado continuam sendo erros; não são ignorados.

`ProtocolComplete` informa conclusão do protocolo remoto. Limite de tokens e filtro de conteúdo continuam sendo motivos de término distintos. Se o callback falhar ao receber `completed`, o protocolo remoto já pode estar completo, embora a operação retorne erro de entrega.

Uso é informado por contador opcional e origem. `Tokens == nil` significa desconhecido; `Tokens != nil` com valor zero e origem `UsageReported` significa zero reportado. O retorno final e os snapshots de eventos são independentes. A estimativa prévia de contexto não substitui uso reportado nem calcula custo.


Exemplo executável: [examples/chat](../../examples/chat/main.go). Declare as capacidades verificadas com `gateway.TextChatCapabilities()` ou `gateway.ChatWithToolsCapabilities()`.
