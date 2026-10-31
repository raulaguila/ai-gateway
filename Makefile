.PHONY: check fmt-check test vet build release-check

check: fmt-check test vet build

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.git/*'))" || (echo 'Run gofmt on Go sources'; exit 1)

test:
	go test -race -timeout 60s ./...

vet:
	go vet ./...

build:
	go build -buildvcs=false ./...

release-check: check
	@test "$$(go list -m)" = 'github.com/raulaguila/ai-gateway' || (echo 'Module path must be github.com/raulaguila/ai-gateway'; exit 1)
	@test -s LICENSE || (echo 'Choose and add the project license first'; exit 1)
