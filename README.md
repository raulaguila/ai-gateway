# AI Gateway para Go

[![CI](https://github.com/raulaguila/ai-gateway/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/raulaguila/ai-gateway/actions/workflows/ci.yml)

Biblioteca Go para chamar OpenAI Compatible/Ollama, Anthropic, Samsung Gauss e Samsung Agent Builder com contratos comuns. Sem servidor, banco ou dependências externas.

## Instalação

O módulo é `github.com/raulaguila/ai-gateway`, sob [licença MIT](LICENSE). A primeira versão é preliminar: `v0.1.0-alpha.1`. Instalar com:

```bash
go get github.com/raulaguila/ai-gateway@v0.1.0-alpha.1
```

Para desenvolver contra a cópia local, usar no `go.mod` do projeto consumidor:

```go
require github.com/raulaguila/ai-gateway v0.0.0
replace github.com/raulaguila/ai-gateway => ../ai-gateway
```

Requer Go 1.22 ou superior. O [roteiro de release](RELEASING.md) descreve a validação e os limites desta versão. A API pode evoluir nas versões preliminares.

## Primeira chamada

Exemplo para um deployment OpenAI Compatible, dentro de uma função que retorna erro e recebe `ctx`, `baseURL`, `apiKey` e o `model` instalado. Importar `gateway "github.com/raulaguila/ai-gateway"` e `"github.com/raulaguila/ai-gateway/providers/openai"`:

```go
provider, err := openai.New(openai.Config{
    BaseURL: baseURL, // incluindo /v1 quando exigido pelo deployment
    APIKey: apiKey,
    ModelCapabilities: map[string]gateway.Capabilities{
        model: gateway.TextChatCapabilities(),
    },
})
if err != nil { return err }
defer provider.CloseIdleConnections()

client, err := gateway.NewSingle(provider, model)
if err != nil { return err }

result, err := client.Chat(ctx, gateway.ChatRequest{
    Messages: []gateway.Message{gateway.User("Olá!")},
    MaxOutputTokens: gateway.Ptr(256),
})
if err != nil { return err }
_ = result.Text
```

Criar e reutilizar provider/client durante o ciclo de vida da aplicação. O perfil declara somente chat e streaming: use capacidades verificadas para seu modelo. Nenhum construtor faz chamadas remotas. Para múltiplos provedores e modelos, usar [deployments e aliases](docs/extending.md).

Um exemplo completo já está pronto para executar:

```bash
GATEWAY_MODEL='modelo-instalado:tag' go run -buildvcs=false ./examples/chat
GATEWAY_MODEL='modelo-instalado:tag' go run -buildvcs=false ./examples/chat -stream
```

A base padrão desse exemplo é `http://localhost:11434/v1` (Ollama). Para outros deployments, configurar `GATEWAY_BASE_URL` e, quando exigida, `GATEWAY_API_KEY`. Substituir a tag pelo ID real disponível. `-buildvcs=false` permite executar também em cópias locais com metadados Git incompletos.

## Streaming de texto

```go
result, err := client.StreamText(ctx, gateway.ChatRequest{
    Messages: []gateway.Message{gateway.User("Explique um AI Gateway.")},
    MaxOutputTokens: gateway.Ptr(256),
}, func(text string) error {
    // Exibir ou encaminhar o delta; retornar erro para interromper a leitura.
    return nil
})
```

O callback é síncrono. `result` contém texto/uso e estado final, inclusive parcial em erro. Usar `Stream` para receber também uso, espera e eventos terminais. Verificar sempre `err`; um resultado parcial não é uma conclusão válida.

## Orçamento de tokens

Adicionar uma política como terceiro argumento de `NewSingle`:

```go
client, err := gateway.NewSingle(provider, model, gateway.TokenPolicy{
    ContextWindow: 32000, // usar a janela real do deployment
    DefaultMaxOutputTokens: 2048,
    SafetyMarginTokens: 512,
    SafetyMarginPercent: 10,
})
```

O contador estimado padrão é automático e pode ser substituído. A validação soma entrada + saída + margem fixa + percentual da entrada arredondado para cima, retornando erro se exceder a janela. Não remove mensagens. A estimativa e as margens não garantem contagem exata.

`client.EstimateBudget(ctx, request)` consulta o orçamento sem gerar. Erros locais `ContextExceeded` incluem os detalhes em `Error.TokenBudget`. Ver [contagem, margens e diagnóstico](docs/tokens.md).

## Provedores e exemplos

| Provedor | Exemplo executável | Configuração e protocolo |
|---|---|---|
| OpenAI Compatible / Ollama | [examples/chat](examples/chat/main.go) | [Referência](docs/providers/openai.md) |
| Anthropic | [examples/anthropic](examples/anthropic/main.go) | [Referência](docs/providers/anthropic.md) |
| Samsung Gauss | [examples/gauss](examples/gauss/main.go) | [Credenciais tipadas e catálogo](docs/providers/gauss.md) |
| Samsung Agent Builder | [examples/samsung](examples/samsung/main.go) | [Fluxo e componentes](docs/providers/samsung.md) |

Todos os exemplos aceitam `-stream`. Ferramentas sem streaming estão disponíveis conforme modelo em OpenAI Compatible, Anthropic e Gauss. Embeddings estão disponíveis somente no adaptador OpenAI Compatible.

## Diagnóstico e extensões

`NewSingle` verifica a configuração local. No gateway completo, chamar `CheckConfiguration(ctx)` explicitamente. Isso detecta capacidades não declaradas e pré-requisitos locais, sem testar credenciais ou conectividade remotas.

`gateway.IsCode` verifica o código; `errors.As` acessa `*gateway.Error`, incluindo deployment, alias, modelo remoto e campo a corrigir. `errors.Is` preserva cancelamento e causas. Ver [erros e transporte](docs/errors-and-transport.md), [extensões](docs/extending.md) e [arquitetura](ARCHITECTURE.md).

## Desenvolvimento

```bash
make check
```

Executa formatação, testes com detector de concorrência, exemplos compilados e `go vet`. Testes usam HTTP/TLS locais; não consomem APIs reais. Os programas em `examples/` só chamam provedores quando executados explicitamente. A validação em deployments reais continua pendente nesta versão preliminar.

O [GitHub Actions](.github/workflows/ci.yml) executa essas verificações no Go `1.22.x` e no alias `stable` (versão estável mais recente), em pushes, pull requests e acionamento manual. `GOTOOLCHAIN=local` garante que o teste de compatibilidade use a versão selecionada, sem atualização automática do compilador. As actions são fixadas por SHA e o workflow usa permissão de leitura do conteúdo.
