.PHONY: test fmt vet tidy check

test:
	go test -race ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy

check: fmt tidy vet test
