.PHONY: dto
dto:
	go run ./cmd/gendto

.PHONY: build
build:
	CGO_ENABLED=0 go build ./...

# Claude 2026-10-21: local CI / ship / nightly GitHub sync (no deploy).
# Reason: live follows local main; origin/main is overnight.
# Review if: scripts/ci-local.sh or sakms-nightly-sync.sh is removed.
.PHONY: test-ci deploy-local ship-local nightly-sync
test-ci:
	./scripts/ci-local.sh

deploy-local:
	./scripts/sakms-deploy-local.sh

ship-local:
	./scripts/sakms-ship-local.sh

nightly-sync:
	./scripts/sakms-nightly-sync.sh

