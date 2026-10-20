.DEFAULT_GOAL := help

.PHONY: help test test-docker lint tidy mod upgrade image-base lint-docker

help: ## Hiển thị danh sách target
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

test: ## Chạy test suite với race detector và coverage
	go test ./... -race -cover

lint: ## Kiểm tra format và chạy go vet
	gofmt -l .
	go vet ./...

tidy: ## Dọn dẹp go.mod và go.sum
	go mod tidy

mod: upgrade ## Cập nhật toàn bộ dependency lên bản mới nhất (alias của upgrade)

upgrade: ## Nâng cấp dependency lên bản mới nhất và chạy test kiểm tra
	go get -u ./...
	go mod tidy
	$(MAKE) test

test-docker: ## Chạy Docker integration test
	AICLI_DOCKER_TESTS=1 go test ./... -race -count=1 -run "Docker|^TestRuntime" -v

# ---- Reference base image (agent CLIs + non-root agent user). Build your own runtime images FROM it. ----
IMAGE_OWNER := aicli
BUILD_ARGS := --build-arg USER_UID=$(shell id -u) --build-arg USER_GID=$(shell id -g)

image-base: ## Build aicli/base
	docker build $(BUILD_ARGS) -t $(IMAGE_OWNER)/base:latest docker/base

lint-docker: ## Lint the Dockerfile with BuildKit's checks
	docker buildx build --check docker/base
