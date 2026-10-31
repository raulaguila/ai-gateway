# Orçamento e validação de tokens

Configurar `ModelRoute.TokenPolicy` para validar `entrada + MaxOutputTokens + SafetyMarginTokens + ceil(entrada * SafetyMarginPercent / 100) <= ContextWindow` em `Gateway.Chat` e `Gateway.Stream`. Sem política, o comportamento anterior é preservado. A configuração é copiada na construção e pode variar entre aliases do mesmo deployment.

Exemplo com o contador estimado padrão e imports `context` e `gateway "github.com/raulaguila/ai-gateway"`:

```go
func withTokenPolicy(provider gateway.Provider, model string) (*gateway.Gateway, error) {
    return gateway.New(gateway.Config{
        Deployments: map[string]gateway.Provider{"local": provider},
        Models: map[string]gateway.ModelRoute{
            "chat": {
                Deployment: "local",
                Model: model,
                TokenPolicy: &gateway.TokenPolicy{
                    ContextWindow: 32000, // usar a janela real do deployment
                    DefaultMaxOutputTokens: 2048,
                    SafetyMarginTokens: 512,
                    SafetyMarginPercent: 10, // 10% dos tokens de entrada
                },
            },
        },
    })
}

func generateWithBudget(ctx context.Context, client *gateway.Gateway) (gateway.ChatResult, error) {
    result, err := client.Chat(ctx, gateway.ChatRequest{
        Model: "chat",
        Messages: []gateway.Message{{Role: gateway.RoleUser, Content: "Olá!"}},
    })
    if gateway.IsCode(err, gateway.ContextExceeded) {
        // A aplicação decide como revisar a entrada ou o orçamento de saída.
        return gateway.ChatResult{}, err
    }
    return result, err
}
```

Quando `CountInputTokens` está vazio (`nil`), a política usa `gateway.EstimateInputTokens` automaticamente. Essa função também pode ser chamada diretamente com `(ctx, request)`. Não usa tokenizer, rede ou dependências externas e preserva a requisição.

`SafetyMarginTokens` reserva uma quantidade fixa adicional de tokens na janela, com padrão zero. Aplica-se tanto à estimativa padrão quanto a contadores personalizados, sem mudar a contagem retornada pelo contador, o `MaxOutputTokens`, as mensagens ou o `Usage`. Por exemplo, entrada 1000 + saída 2000 + margem 500 exige janela de pelo menos 3500. A margem deve ser não negativa e menor que a janela; um padrão de saída que, somado à margem, ultrapasse a janela é configuração inválida. A margem reduz o espaço disponível para compensar diferenças da estimativa, mas não garante que a contagem real caiba. Erros de excesso informam também a margem utilizada.

`SafetyMarginPercent` acrescenta uma margem sobre os tokens de **entrada**, com arredondamento para cima: `10` significa 10%, e entrada de 1001 tokens reserva mais 101. Aceita percentuais inteiros não negativos, inclusive acima de 100, com padrão zero. A porcentagem não incide sobre saída ou margem fixa. As margens podem ser usadas isoladamente ou somadas: entrada 1001 + saída 2000 + margem fixa 500 + margem de 10% (101) exige janela de pelo menos 3602. A validação aplica ambas a estimativas e contadores personalizados, sem modificar o pedido ou o uso reportado. Não há redução automática das margens; excesso retorna `context_exceeded` com o percentual informado. A configuração não antecipa a margem percentual, que depende da entrada de cada chamada.

**É uma estimativa, não uma contagem exata nem um limite superior garantido.** A heurística usa `ceil(bytes UTF-8 / 3)` por campo não vazio, mais 3 tokens para iniciar a resposta, 4 por mensagem (incluindo `System` não vazio) e 8 por definição de ferramenta ou chamada. Inclui papéis, conteúdo, IDs, nomes, descrições, schemas e argumentos/resultados de ferramentas. Modelo e parâmetros de geração não entram na entrada estimada; a reserva de saída é somada separadamente pela política. A contagem pode ficar acima ou abaixo do uso real dependendo do idioma, tokenizer e formato do provedor. Erros de excesso calculados pelo padrão identificam `estimated input tokens`; a estimativa nunca preenche `Usage` como uso reportado.

Para maior precisão, configurar `CountInputTokens: meuContador`. Esse contador tem prioridade sobre o padrão e recebe o ID remoto resolvido e o orçamento efetivo. Deve considerar `System`, todas as mensagens, definições de ferramentas, chamadas/resultados e overhead do protocolo/modelo, respeitar o contexto, suportar concorrência e tratar a requisição como somente leitura. A janela deve corresponder ao deployment real. Conteúdo acrescentado remotamente pelo Agent Builder não é conhecido pelo estimador padrão e precisa ser considerado pelo consumidor.

`MaxOutputTokens` explícito prevalece sobre `DefaultMaxOutputTokens`. Se ambos estiverem ausentes/zero, a chamada retorna `invalid_request`, sem assumir o padrão do servidor. Um padrão positivo da política é enviado ao adaptador como `MaxOutputTokens`, mantendo reserva e limite transmitido iguais. Adaptadores que não suportem esse parâmetro continuam retornando erro; no Agent Builder é necessário configurar `LLMComponentID`.

O limite exato é aceito. Excesso retorna `context_exceeded`, com deployment/operação e contagens, antes do dispatch e sem eventos de streaming. Erro do contador ou contagem negativa interrompem a chamada; cancelamento e prazo mantêm seus códigos. Não há remoção de mensagens, resumo, redução automática de saída ou consulta implícita ao catálogo. Embeddings, catálogo e chamadas diretas aos adaptadores não passam por essa política. A contagem prévia não altera `Usage`, que continua refletindo somente os dados de uso retornados pelo provedor.

Erros locais de `ContextExceeded` expõem `Error.TokenBudget`, sem exigir interpretação da mensagem. Exemplo com imports `errors` e `gateway "github.com/raulaguila/ai-gateway"`:

```go
func budgetFromError(err error) *gateway.TokenBudget {
    var detail *gateway.Error
    if errors.As(err, &detail) && detail.Code == gateway.ContextExceeded {
        return detail.TokenBudget
    }
    return nil
}
```

O orçamento contém `ContextWindow`, `InputTokens`, `ReservedOutputTokens`, `SafetyMarginTokens`, `SafetyMarginPercent`, `PercentageMarginTokens` (parcela percentual arredondada) e `TotalTokens` (soma necessária). `InputEstimated` identifica o estimador padrão; `false` indica contador personalizado e não garante precisão. Não contém prompts, credenciais ou respostas, nem substitui `Usage`.

Os campos de entrada, parcela percentual e total são ponteiros: verificar `nil` antes de acessar. Se saída + margem fixa já excederem a janela, o contador é dispensado e esses três campos ficam ausentes. Zero conhecido continua sendo um ponteiro para zero. `Overflow: true` indica que um componente ou soma ultrapassou o intervalo de `int`; valores calculáveis permanecem disponíveis e valores não representáveis ficam ausentes, sem saturação ou números negativos. Cada erro possui seu próprio orçamento. Erros retornados pelo provedor e falhas de contagem não recebem esse orçamento local.


## Consultar antes de gerar

```go
budget, err := client.EstimateBudget(ctx, request)
if err != nil {
    // ContextExceeded também devolve o orçamento disponível em budget.
    return err
}
if budget.TotalTokens != nil {
    available := budget.ContextWindow - *budget.TotalTokens
    _ = available
}
```

`EstimateBudget` resolve o modelo, aplica o mesmo padrão de saída, valida a requisição e os pré-requisitos locais, e usa o mesmo cálculo da geração. Não chama catálogo nem geração e não exige inferência real para funcionar. O contador padrão é local; contadores personalizados mantêm sua própria política de I/O. É necessário configurar `TokenPolicy`. A consulta não executa o teste de capacidades de chat/stream; ela também pode ser usada antes de declarar essas capacidades. A contagem é refeita ao gerar, pois a requisição ou o contador podem mudar.
