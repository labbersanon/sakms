.PHONY: dto
dto:
	go run ./cmd/gendto

.PHONY: build
build:
	CGO_ENABLED=0 go build ./...
