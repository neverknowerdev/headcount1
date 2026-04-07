.PHONY: build dev setup

setup:
	cd frontend && npm install

build-frontend:
	cd frontend && npm run build

build: build-frontend
	go build -o orchestrator main.go

dev:
	@echo "Run 'cd frontend && npm run dev' in one terminal"
	@echo "Run 'go run main.go' in another terminal"
