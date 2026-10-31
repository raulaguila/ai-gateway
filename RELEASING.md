# Distribuição e release

Módulo `github.com/raulaguila/ai-gateway`, sob [licença MIT](LICENSE). A primeira versão é `v0.1.0-alpha.1`, preliminar. O repositório público é [raulaguila/ai-gateway](https://github.com/raulaguila/ai-gateway), com branch padrão `develop`.

```bash
go get github.com/raulaguila/ai-gateway@v0.1.0-alpha.1
```

A versão inclui núcleo, quatro adaptadores, políticas de tokens, exemplos executáveis e documentação. O documento de entrada de planejamento e metadados locais de ferramentas não integram a distribuição.

## Validação e limites

`make release-check` executa formatação, testes com `-race`, exemplos Go testáveis, `go vet` e compilação, e verifica o módulo e a licença. O sucesso desse comando não cria uma release.

Os testes usam HTTP/TLS locais, sem consumo de APIs reais. Samsung/Gauss usam formatos conferidos em parsers e fixtures do Thoth; compatibilidade com deployments reais ainda precisa de validação. A versão mínima declarada é Go 1.22; a execução desta primeira release usa Go 1.27.0. A API pode mudar durante a fase preliminar.

## Publicar próximas versões

1. Trabalhar em um checkout válido do repositório, revisar o changelog e atualizar a versão na documentação quando necessário.
2. Executar `make release-check`, sem credenciais reais nos testes.
3. Validar separadamente os deployments reais com credenciais fornecidas por ambiente. Exemplos fazem geração e podem consumir cota.
4. Publicar o commit revisado e criar uma tag de versão semântica apontando para esse commit, sem reescrever tags existentes. Marcar versões `alpha`, `beta` ou `rc` como pré-release no GitHub.
5. Verificar `go get github.com/raulaguila/ai-gateway@<versão-publicada>` a partir de outro módulo sem `replace`. Se for público, o módulo pode ser obtido pelo proxy Go ou diretamente do repositório.

Projetos que usavam o módulo temporário `ai-gateway` devem atualizar imports, `require` e `replace` para o caminho definitivo. Para desenvolvimento local, consultar o README.
