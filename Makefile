.PHONY: build dev setup e2e run run-dev

setup:
	cd frontend && npm install

build-frontend:
	cd frontend && npm run build

build: setup build-frontend
	go build -o agent-orchestrator main.go

run: build-frontend
	go run .

run-dev:
	@if pid=$$(lsof -ti :8080); then \
		echo "Port 8080 in use by PID $$pid, killing..."; \
		kill -9 $$pid; \
		sleep 1; \
	fi
	@trap 'kill 0' EXIT; \
	go tool air & \
	cd frontend && npm run dev

dev:
	@echo "Run 'cd frontend && npm run dev' in one terminal"
	@echo "Run 'go run main.go' in another terminal"

e2e: build-frontend
	cd e2e && npm run test
