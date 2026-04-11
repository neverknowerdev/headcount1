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
./orchestrator &
PID=$!
sleep 2
cd e2e
npx playwright test
cd ..
kill $PID
