# Include environment variables
-include .env

# Directories
TMP_DIR := tmp
COVER_DATA_DIR ?= $(TMP_DIR)/test/cov/data
COVER_REPORT_DIR ?= $(TMP_DIR)/test/cov
COVER_REPORT_NAME ?= test-report.html
MYSQL_HOST := 0.0.0.0
SESSION_PROVIDER ?= mysql
GO_TEST_FLAGS ?= -race -coverprofile=$(COVER_DATA_DIR)/coverage.out
VERSION := $(shell git tag | sort -V | tail -1)
GOOSE_DIR = ./internal/db/migrations/$(SESSION_PROVIDER)
GOOSE_DSN = "$(MYSQL_USER):$(MYSQL_PASSWORD)@tcp($(MYSQL_HOST):3306)/$(MYSQL_DATABASE)?parseTime=true"
SCOPE ?= minor

# Define all phony targets
.PHONY: init init-hooks clean generate build \
        init-test-bin build-test-bin coverage-report-bin coverage-report \
        test-bin test-it test test-it-debug test-debug init-test lint \
        dev-build  dev dev-stop build-docker run-docker release version

# Main targets
init: clean init-hooks

clean:
	docker compose down -v

init-hooks:
	find .githooks/ -type f ! -name "*.sample" -exec chmod +x {} \;
	find .git/hooks -type l -exec rm {} \;
	find .githooks -type f -exec ln -sf ../../{} .git/hooks/ \;

generate:
	@mkdir -p $(TMP_DIR) $(TMP_DIR)/mysql_data
	go generate ./...

# Build targets
build: generate
	$(MAKE) version
	go build $(BUILDARGS) $(PKGARGS) -o ./$(TMP_DIR)/opencode-$(VERSION) ./main.go

dev-build: generate
	REGISTRY_URL=docker.io docker compose up -d --build

dev: generate 
	@set -a; . .env; set +a; \
	REGISTRY_URL=docker.io docker compose up -d; \
	docker compose logs -f --since 0s mysql
	@echo "Logging..."

dev-stop:
	docker compose stop

# Test targets
init-test-bin:
	rm -rf $(COVER_DATA_DIR)
	mkdir -p $(COVER_DATA_DIR)

build-test-bin:
	BUILDARGS="-cover" $(MAKE) build
	$(MAKE) init-test-bin

coverage-report-bin:
	go tool covdata percent -i=$(COVER_DATA_DIR)
	go tool covdata textfmt -i=$(COVER_DATA_DIR) -o=$(COVER_REPORT_DIR)/test-bin-report.txt
	go tool cover -func=$(COVER_REPORT_DIR)/test-bin-report.txt
	go tool cover -html=$(COVER_REPORT_DIR)/test-bin-report.txt

coverage-report:
	go tool cover -func=$(COVER_DATA_DIR)/coverage.out | grep total
	go tool cover -html=$(COVER_DATA_DIR)/coverage.out -o $(COVER_REPORT_DIR)/$(COVER_REPORT_NAME)

init-test: init-test-bin generate

test-it: init-test lint
	go test $(GO_TEST_FLAGS) -tags=integration ./...
	$(MAKE) COVER_REPORT_NAME=test-it.report.html coverage-report

test: init-test lint
	go test $(GO_TEST_FLAGS) -short ./...
	$(MAKE) coverage-report

test-debug:
	$(MAKE) GO_TEST_FLAGS="$(GO_TEST_FLAGS) -v" test

lint:
	go fmt ./...
	go vet ./...

mysql-cli:
	@mysql --host=$(MYSQL_HOST) --port=3306 --user=$(MYSQL_USER) --password=$(MYSQL_PASSWORD) --database=$(MYSQL_DATABASE)

goose-%:
	@goose -dir $(GOOSE_DIR) $(SESSION_PROVIDER) $(GOOSE_DSN) $*

spec-%:
	@touch "./spec/$$(date +"%Y%m%dT%H%M%S")-$*.md"

# Release targets
version:
	@echo $(VERSION)

release:
	@./scripts/release --$(SCOPE)

