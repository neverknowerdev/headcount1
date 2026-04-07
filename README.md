# Agent Orchestrator MVP

This is an MVP implementation of an agent orchestration system. It is distributed as a single Go binary with an embedded React frontend.

## Prerequisites
- **Go**: >= 1.21
- **Node.js**: >= 18 (and `npm`)
- **PostgreSQL**: A running instance (version 13+)

## Local Build & Run Instructions

### 1. Database Setup
Ensure PostgreSQL is running and create a database named `orchestrator`:
```sh
psql -U postgres -c "CREATE DATABASE orchestrator;"
```

Then, initialize the database schema:
```sh
psql "postgres://postgres:postgres@localhost:5432/orchestrator?sslmode=disable" -f db/migration/001_init.sql
```

### 2. Building the Project
You can build the single binary containing both the frontend and backend with our provided Makefile:

```sh
# This will install frontend dependencies, build the React app, and compile the Go binary
make build
```

This creates an executable file named `orchestrator`.

### 3. Running the Server
You can run the generated binary directly. By default, it will look for PostgreSQL at `postgres://postgres:postgres@localhost:5432/orchestrator?sslmode=disable`.

If your database connection string is different, set the `DATABASE_URL` environment variable:
```sh
export DATABASE_URL="postgres://username:password@localhost:5432/orchestrator?sslmode=disable"
./orchestrator
```

The server will start on port `8080`. You can access the UI at http://localhost:8080.
