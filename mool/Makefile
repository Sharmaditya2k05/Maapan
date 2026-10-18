.PHONY: test vet fmt tidy release

test:   ## Unit tests
	go test ./...

vet:    ## go vet + gofmt check
	go vet ./... && test -z "$$(gofmt -l .)"

fmt:
	gofmt -w .

tidy:
	go mod tidy

release: vet test  ## Tag a version: make release V=v0.2.0
	@test -n "$(V)" || (echo "usage: make release V=v0.2.0" && exit 1)
	git tag $(V) && git push origin $(V)
