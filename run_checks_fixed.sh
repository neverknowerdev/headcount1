#!/bin/bash
set -e
echo "Running Go tests..."
go test ./...

echo "Running Go vet..."
go vet ./...

echo "Running Frontend lint..."
cd frontend
npm run lint
cd ..

echo "Running Playwright E2E..."
rm -f orchestrator.db
kill $(lsof -t -i :8080) 2>/dev/null || true
./orchestrator &
PID=$!
sleep 2
cd e2e
npx playwright test tests/app.spec.ts
cd ..
kill $PID
