.DEFAULT_GOAL := help

.PHONY: help verify test test-docker lint tidy mod upgrade image-base lint-docker web-install web-lint web-test web-build web-verify web-example tools

help: ## Hiển thị danh sách target
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

test: ## Chạy test suite với race detector và coverage
	go test ./... -race -cover

verify: lint test web-verify web-example ## Chạy lint và test cho Go và web (cổng kiểm tra trước khi hoàn tất)

lint: ## Kiểm tra format (fail nếu có file chưa gofmt) và chạy go vet
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: các file trên chưa được format"; exit 1; }
	go vet ./...

tidy: ## Dọn dẹp go.mod và go.sum
	go mod tidy

mod: upgrade ## Cập nhật toàn bộ dependency lên bản mới nhất (alias của upgrade)

upgrade: ## Nâng cấp dependency lên bản mới nhất và chạy test kiểm tra
	go get -u $$(go list ./...)
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

# ---- web/: @aicli/ui (API client + React components), pnpm ----
web-install: ## Cài dependency của web/ (đúng lockfile, chặn script cài đặt)
	cd web && pnpm install --frozen-lockfile --ignore-scripts

web-lint: web-install ## Typecheck và eslint cho web/
	cd web && pnpm run typecheck && pnpm run lint

web-test: web-install ## Chạy test của web/
	cd web && pnpm run test

web-build: web-install ## Build web/ rồi kiểm tra dist, exports (publint, attw)
	cd web && pnpm run build

web-verify: web-lint web-test web-build ## Toàn bộ kiểm tra của web/

web-example: web-build ## Build examples/web (Vite + React) với @aicli/ui vừa build, để biết ví dụ không bị hỏng
	cd examples/web && pnpm install --frozen-lockfile --ignore-scripts && pnpm run build

# ---- Host-installed CLIs (package toolchain) ----
tools: ## Build the aicli-tools command into bin/
	go build -o bin/aicli-tools ./cmd/aicli-tools
