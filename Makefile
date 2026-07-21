.PHONY: build dev setup e2e run run-dev

COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE   := $(shell date -u +%Y-%m-%d)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
LDFLAGS := -X main.CommitHash=$(COMMIT) -X main.BuildDate=$(DATE) -X main.Branch=$(BRANCH)

setup:
	cd frontend && npm install

build-frontend:
	cd frontend && npm run build

build: setup build-frontend
	go build -ldflags "$(LDFLAGS)" -o agent-orchestrator main.go

run: build-frontend
	go run .

run-dev:
	@if pid=$$(lsof -ti TCP:8080 -sTCP:LISTEN); then \
		echo "Port 8080 in use by PID $$pid, killing..."; \
		kill -9 $$pid; \
		sleep 1; \
	fi
	@trap 'kill 0' EXIT; \
	HEADCOUNT1_LOCAL_BOOTKEY=1 go tool air & \
	cd frontend && npm run dev

dev:
	@echo "Run 'cd frontend && npm run dev' in one terminal"
	@echo "Run 'go run main.go' in another terminal"

e2e: build-frontend
	cd e2e && npm run test
