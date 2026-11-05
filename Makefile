.PHONY: help dev dev-api dev-frontend frontend build run test sqlc clean

help:
	@echo 'Targets:'
	@echo '  dev           run Go API and Nuxt dev server together (Ctrl-C stops both)'
	@echo '  dev-api       run the Go API alone (no embedded SPA)'
	@echo '  dev-frontend  run the Nuxt dev server alone (proxies API to localhost:8888)'
	@echo '  frontend      build the SPA and stage it under internal/web/dist/'
	@echo '  build         frontend + go build → ./transcriber (single binary)'
	@echo '  run           build then run ./transcriber'
	@echo '  test          go test -race ./...'
	@echo '  sqlc          regenerate internal/db/dbq from internal/db/{migrations,queries}'
	@echo '  clean         remove build artifacts'

# Run both servers in parallel. `set -m` puts each background recipe in its
# own process group; the trap then signals each group (`kill -- -PGID`) so
# Ctrl-C tears down go run + its compiled child + nuxt + nitro together.
SHELL := /bin/bash

dev:
	@echo '→ API   http://localhost:8888'
	@echo '→ Nuxt  http://localhost:3000  (proxies API)'
	@set -m; \
	  $(MAKE) -s dev-api & API_PID=$$!; \
	  $(MAKE) -s dev-frontend & WEB_PID=$$!; \
	  trap "kill -TERM -- -$$API_PID -$$WEB_PID 2>/dev/null; wait" INT TERM; \
	  wait

dev-api:
	go run ./cmd/transcriber

dev-frontend:
	cd frontend && pnpm install && pnpm dev

# Build the SPA and stage the output where //go:embed can pick it up.
frontend:
	cd frontend && pnpm install && pnpm generate
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -R frontend/.output/public/. internal/web/dist/

build: frontend
	go build -o transcriber ./cmd/transcriber

run: build
	./transcriber

test:
	go test -race ./...

sqlc:
	sqlc generate

clean:
	rm -f transcriber
	rm -rf frontend/.nuxt frontend/.output
	# keep the placeholder index.html so //go:embed compiles on next build
	find internal/web/dist -mindepth 1 ! -name index.html ! -name .gitignore -exec rm -rf {} +
