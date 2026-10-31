# Capacidades, aliases e extensões

`Provider` exige apenas `Capabilities(model string) Capabilities`, sem acesso à rede. `ChatProvider`, `StreamProvider`, `EmbeddingProvider` e `CatalogProvider` acrescentam as operações opcionais. Adaptadores externos precisam importar somente os contratos públicos e registrar suas instâncias em `Config.Deployments`; o núcleo não importa os adaptadores.

O estado zero de uma capacidade é `Unknown`. O gateway rejeita capacidades desconhecidas por padrão; para tentar uma operação ainda não verificada, configurar explicitamente `AllowUnknownCapabilities: true`. Uma capacidade `Unsupported` ou interface ausente sempre impede o dispatch. Não declarar suporte somente porque o adaptador possui o método.

OpenAI Compatible e Anthropic também permitem chamadas diretas: nelas, capacidades explicitamente negadas são rejeitadas, e capacidades desconhecidas podem ser tentadas contra o servidor. Usar a fachada para a política estrita. Nesses adaptadores, o mapa `ModelCapabilities` substitui integralmente as capacidades padrão para um modelo, sem merge; isso evita herdar acidentalmente suporte de outro modelo. No Gauss, inferência exige um snapshot nativo válido, descrito abaixo.

`ChatRequest.Model` e `EmbedRequest.Model` são aliases na fachada e IDs remotos no adaptador direto; no Gauss, usar o GUID do catálogo. `ListModels(ctx, deployment)` faz descoberta explícita. A descoberta não altera os aliases do gateway. Gauss mantém seu próprio snapshot de roteamento e capacidades declaradas pelo catálogo, atualizado apenas por refresh explícito; listagem não comprova geração real.


## Facilidades e validação local

`NewSingle(provider, model)` registra um provedor/modelo e permite omitir `ChatRequest.Model` e `EmbedRequest.Model`. Um terceiro argumento opcional `TokenPolicy` habilita orçamento. Uma seleção explícita continua precisando corresponder ao alias registrado (o próprio ID do modelo). `NewSingle` verifica a configuração local; não promove capacidades desconhecidas ou realiza I/O.

`TextChatCapabilities()` declara chat e streaming; `ChatWithToolsCapabilities()` declara também ferramentas sem streaming. São declarações explícitas do consumidor sobre modelos verificados, sem habilitar catálogo ou embeddings. O gateway completo mantém `AllowUnknownCapabilities` como opt-in.

Provedores podem implementar `ChatRequestValidator` para checar pré-requisitos locais sem rede e sem modificar a requisição. A fachada passa o modelo remoto e o padrão efetivo de saída. A verificação de configuração usa uma mensagem representativa; não garante validade de todos os pedidos futuros. O validador precisa ser seguro para concorrência. Interfaces de provedores existentes continuam válidas.

Use `CheckConfiguration(ctx)` após configurar aliases ou atualizar catálogos para detectar pré-requisitos e ausência de operações de inferência. A verificação não conta tokens ou consulta APIs.

`Ptr(value)` facilita campos opcionais sem confundir zero com ausência; `User(text)` constrói uma mensagem de usuário. `StreamText` entrega somente deltas textuais, mas mantém o resultado final, retorno de erros e cancelamento. Para uso, espera e eventos completos, use `Stream`. Chamadas diretas aos adaptadores não aplicam a política do gateway.
