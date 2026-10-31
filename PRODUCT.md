# Product

<!-- keel:product-schema 1 -->

## Platform

Biblioteca Go para integração com provedores de IA. Implementa núcleo extensível, aliases, adaptador OpenAI Compatible para chat/streaming/ferramentas/embeddings/catálogo e adaptadores Anthropic e Gauss para chat/streaming/ferramentas/catálogo, sem embeddings. Gauss inclui roteamento nativo e cota compartilhada no processo.

Samsung Agent Builder implementa geração buffered/NDJSON e catálogo local; não implementa ferramentas normalizadas ou embeddings.

A API oferece inicialização simples por `NewSingle`, perfis explícitos de capacidades, helpers de mensagens/parâmetros e streaming textual. Há verificação local de configuração e prévia do orçamento de tokens, sem geração. Gauss aceita credenciais tipadas e permite inspecionar/atualizar explicitamente o catálogo. A referência foi separada por tarefa/provedor; exemplos executáveis e testáveis cobrem a jornada inicial. O autor definiu o módulo público `github.com/raulaguila/ai-gateway` e a licença MIT. A primeira versão de distribuição é `v0.1.0-alpha.1`, preliminar, com validação por fixtures e HTTP/TLS locais.

## Stack

Go, apenas biblioteca padrão. `go.mod` declara Go 1.22; ambiente disponível de verificação é Go 1.27.0. O módulo é `github.com/raulaguila/ai-gateway`, distribuído sob licença MIT. Sem banco ou infraestrutura obrigatória.

## Users

O autor e seus projetos consumidores. A intenção inicial confirmada é distribuir uma biblioteca. As linguagens dos demais projetos consumidores ainda não foram informadas.

## Product Purpose

Permitir que projetos usem diferentes provedores por um contrato consistente de geração, streaming, ferramentas e embeddings, preservando as diferenças relevantes de cada protocolo.

## Positioning

Biblioteca de integração e políticas de inferência. O projeto consumidor mantém a responsabilidade pela conversa, RAG, execução de ferramentas, armazenamento e gestão das credenciais. Um serviço HTTP pode ser construído sobre a biblioteca futuramente.

## Operating Context

A biblioteca será executada dentro de cada aplicação Go consumidora. Distribuição entre processos, infraestrutura de implantação e requisitos de compartilhamento de cotas ainda precisam ser definidos.

## Scale & SLOs

Desconhecidos; confirmar antes de projetar para volume, disponibilidade ou latência específicos.

## Cost Posture

Orçamento desconhecido. Requisições reais, sondagem de embeddings e descoberta que envolva inferência podem consumir recursos. Uso ausente deve continuar desconhecido, sem ser convertido em custo zero.

## Capabilities and Constraints

- O documento descreve Samsung Agent Builder, Samsung Gauss, Ollama, OpenAI Compatible, OpenAI e Anthropic. No escopo proposto atualizado, Ollama será atendido pelo perfil OpenAI Compatible; não haverá adaptador Ollama nativo inicialmente.
- Capacidades dependem do adaptador e do modelo/deployment; não são universais.
- Novos provedores devem poder ser implementados em módulos externos por contratos públicos, sem modificar o núcleo. A proposta usa uma interface base comum e interfaces opcionais por capacidade.
- Preservar `insecure_tls` por deployment OpenAI Compatible, com padrão `false` e isolamento do cliente HTTP.
- Suportar cancelamento, limites de contexto, conclusão explícita de streams e uso com origem identificada.
- Ferramentas são descritas e traduzidas pela biblioteca, mas executadas pelo consumidor.
- O documento descreve o Thoth. Por orientação do usuário, foram consultados o projeto Thoth, seus parsers e testes. A fixture Gauss foi incorporada aos testes deste repositório; Agent Builder reproduz os formatos dos testes originais.
- A biblioteca valida parâmetros, capacidades e, opcionalmente, o orçamento de tokens por alias: entrada estimada pelo padrão ou contada pelo consumidor + saída explícita + margem fixa + percentual configurável da entrada não pode exceder a janela configurada. `SafetyMarginTokens` e `SafetyMarginPercent` têm padrão zero; o percentual inteiro é arredondado para cima e somado à margem fixa, sem alterar o pedido ou uso reportado. Excesso é rejeitado antes do provedor, sem cortar ou resumir histórico. O erro local expõe `TokenBudget` com entrada estimada ou personalizada, saída reservada, margens, total e janela, distinguindo valores não calculados e overflow, sem conteúdo das mensagens. O contador padrão `EstimateInputTokens` é uma heurística sem tokenizer e sem garantia de limite superior; pode ser substituído. Janela real e adequação da contagem pertencem ao consumidor. Estimativa de contexto não substitui uso reportado. Limites gerais de concorrência, quotas distribuídas e hooks gerais permanecem no plano. Gauss implementa a regra local de 3 gerações/minuto descrita no documento, com retry limitado de 429 e espera cancelável.
- Os payloads Samsung/Gauss foram conferidos no código e fixtures do Thoth, sem chamar os deployments reais. Confirmar conectividade e versão do deployment antes de considerar a integração remota validada.
- Bases HTTP são configuração confiável da aplicação; filtro de IP/DNS para URLs de usuários não confiáveis ainda não faz parte do transporte padrão.

## Evidence on Hand

- Documento original de requisitos (material de planejamento local, fora da distribuição), referência técnica datada de 2026-10-30.
- Pedido do usuário por Go e esclarecimento de que pretende distribuir uma biblioteca.
- [Proposta de arquitetura](docs/decisions/0001-biblioteca-ai-gateway.md).

## Product Principles

1. Contrato comum com capacidades e diferenças explícitas.
2. Dependências e infraestrutura mínimas para quem importa a biblioteca.
3. Sem repetição silenciosa de gerações ou execução de ferramentas.
4. Credenciais, dados de conversa e políticas de retenção pertencem ao consumidor.
5. Comportamento verificável com fixtures locais antes de chamadas reais.
