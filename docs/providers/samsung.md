# Samsung Agent Builder

Importar `github.com/raulaguila/ai-gateway/providers/samsung`. `Chat` usa `POST {base}/api/v1/run/{flow}?stream=false` e lê JSON final; `Stream` usa `?stream=true` com `Accept: application/x-ndjson`. O modelo remoto é a chave do fluxo. A chave selecionada na requisição prevalece sobre `Config.AgentKey`; chamadas diretas com `Model` vazio podem usar esse fallback. Na fachada, aliases precisam apontar para uma chave explícita.

No exemplo de construção, os IDs abaixo devem corresponder a componentes reais do fluxo:

```go
provider, err := samsung.New(samsung.Config{
    BaseURL: baseURL,
    APIKey: apiKey,
    AgentKey: flowID,
    ChatInputID: chatInputID,
    ChatOutputID: chatOutputID,
    LLMComponentID: llmComponentID,
    MemoryComponentID: memoryComponentID,
})
```

Tratar o erro, fechar conexões ociosas no encerramento e registrar o provider/alias como nos outros exemplos. A API key é enviada em `x-api-key`; pode ser vazia para deployments que permitam isso. A base deve incluir o prefixo anterior a `/api`, sem query ou credenciais embutidas. Chaves de fluxo são segmentos únicos; escapes de rota, `/`, `..` e separadores são rejeitados.

`input_type` e `output_type` assumem `chat`; `Config.InputType/OutputType` permitem ajustar esses valores para um fluxo que continue devolvendo o formato textual suportado. O diálogo vira `role: conteúdo`, separado por linhas em branco, com `System` no início e separador `---`. `Config.SendHistory` assume true; um ponteiro para false seleciona somente a última mensagem `user`, preservando `System`. O contrato atual não tem a marca `Live` do Thoth; quando necessário, o consumidor seleciona previamente as mensagens.

Somente componentes configurados recebem `component_inputs`: chat input/output têm `session_id=""` e `should_store_message=false`; output recebe `sender_name="AI"`, input recebe `Config.UserName` quando informado; memória recebe `n_messages=0` e `order="Ascending"`. IDs duplicados são rejeitados para evitar sobrescrita de controles. O payload não contém `session_id` superior. Esses ajustes não verificam a topologia remota: o consumidor precisa configurar os IDs corretos e confirmar que o fluxo não adiciona outra memória.

O componente LLM recebe `parameters` como uma **string JSON**: defaults `temperature=0.3`, `top_p=0.95` e `extra_body.repetition_penalty=1.05`; `MaxOutputTokens` vira `max_tokens` quando definido. Zero explícito de temperatura/top-p é preservado, conforme decisão do ADR, corrigindo a substituição por default existente no legado. Se forem solicitados parâmetros sem `LLMComponentID`, a biblioteca retorna `unsupported_capability` antes da rede, em vez de ignorá-los silenciosamente.

O formato buffered verificado no Thoth é:

```json
{"session_id":"session-1","outputs":[{"outputs":[{"results":{"message":{"text":"Resposta"}}}]}]}
```

O primeiro texto não branco em `outputs[].outputs[].results.message.text` é a resposta. `ChatResult.SessionID` preserva a sessão observada por chamada, sem estado global no provider e sem reutilização automática em requisições futuras. Uso permanece desconhecido; o sucesso marca `FinishStop` e `ProtocolComplete=true`, sem inventar um motivo nativo.

O streaming usa os eventos confirmados nos parsers e testes do Thoth:

```json
{"event":"token","data":{"chunk":"Olá"}}
{"event":"add_message","data":{"session_id":"session-1"}}
{"event":"end","data":{"result":{"session_id":"session-1","outputs":[{"outputs":[{"results":{"message":{"text":"Olá mundo"}}}]}]}}}
```

`token` produz deltas; `add_message` atualiza apenas a sessão, sem exibir seu texto como resposta. `end` é obrigatório e pode trazer o resultado final. Um final que estende o prefixo entrega apenas o sufixo; um final divergente ou encurtado produz `invalid_response`, preservando os deltas entregues e sem `completed`. Isso evita a substituição silenciosa do texto exibido que existia no Thoth. Aceita `end` sem resultado após tokens válidos e o último registro sem newline. Conteúdo vazio falha. Eventos desconhecidos são ignorados, mas JSON ou payloads de eventos conhecidos inválidos falham com erro sanitizado; não são ignorados como no legado. EOF sem `end` produz `stream_incomplete`.

`ListModels` é **local**: devolve o fluxo de `AgentKey` ou lista vazia. Não faz HTTP e não testa credenciais, conexão ou execução. Ferramentas normalizadas e histórico de ferramentas são rejeitados; embeddings não são implementados. Não há retentativas automáticas. O provider é seguro para chamadas concorrentes, e cada resultado conserva sua própria sessão.

Prazos padrão: 5 minutos para buffered e 30 minutos para stream, configuráveis e subordinados ao contexto. Limite total padrão de 8 MiB; `MaxLineBytes` assume 4 MiB porque `end` pode carregar o resultado completo. TLS validado, transportes isolados e redirects bloqueados seguem os demais adaptadores. O streaming é interpretado como NDJSON sem depender de um Content-Type específico do servidor, conforme o cliente original.

Os testes reproduzem os payloads de `backend/internal/adapter/outbound/llm/samsung_stream_test.go` do Thoth. Foram conferidos também seus structs e parsers em `samsung.go`. São testes locais; não confirmam um deployment Samsung real. O perfil Samsung usa `component_inputs` descrito no documento e verificado no Thoth, sem substituir esse contrato pelos endpoints mais novos do Langflow público.


Exemplo executável: [examples/samsung](../../examples/samsung/main.go). Definir `GATEWAY_BASE_URL`, `GATEWAY_MODEL` (fluxo) e `SAMSUNG_LLM_COMPONENT_ID`. Chave e IDs de chat/memória são opcionais conforme o fluxo. `NewSingle` detecta a ausência do componente LLM quando uma política de tokens o exige. O exemplo sempre envia limite de saída e, por isso, exige o ID LLM. Aceita `-stream`.
