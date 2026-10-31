# Changelog

## v0.1.0-alpha.1 — 2026-10-31

Primeira versão preliminar. Provedores validados com HTTP/TLS locais e fixtures; deployments reais ainda não foram testados.

- Núcleo extensível com aliases, capacidades e adaptadores OpenAI Compatible, Anthropic, Samsung Gauss e Samsung Agent Builder.
- Chat, streaming, ferramentas sem streaming e embeddings conforme as capacidades de cada integração.
- Política opcional de tokens com estimativa padrão, margens fixa/percentual e orçamento estruturado nos erros locais.
- `NewSingle`, perfis explícitos de capacidades, `User`, `Ptr`, `StreamText`, `EstimateBudget` e verificação local de configuração.
- Credenciais tipadas, estado do catálogo e atualização explícita no Gauss, preservando o formato legado de credenciais.
- Erros com alias, modelo remoto, campo a corrigir e pré-requisitos locais dos provedores.
- Exemplos executáveis por provedor, exemplos compilados por testes e documentação por tarefa/protocolo.

- Módulo público `github.com/raulaguila/ai-gateway` e licença MIT definidos pelo autor; imports e documentação migrados.

Go mínimo declarado: 1.22. Validação desta versão com Go 1.27.0.
