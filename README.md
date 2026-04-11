# Agent Orchestrator MVP

This is an MVP implementation of an agent orchestration system. It is distributed as a single Go binary with an embedded React frontend.

## Prerequisites
- **Go**: >= 1.21
- **Node.js**: >= 18 (and `npm`)

## Local Build & Run Instructions

### 1. Building the Project
You can build the single binary containing both the frontend and backend with our provided Makefile:

```sh
# This will install frontend dependencies, build the React app, and compile the Go binary
make build
```

This creates an executable file named `orchestrator`.

### 2. Running the Server
You can run the generated binary directly. By default, it will create a local SQLite database named `orchestrator.db` and perform automatic migrations on startup!

```sh
./orchestrator
```

**PostgreSQL Support (Optional)**:
If you prefer to use an external PostgreSQL database, you can supply a Postgres connection string via the `DATABASE_URL` environment variable:
```sh
export DATABASE_URL="postgres://username:password@localhost:5432/orchestrator?sslmode=disable"
./orchestrator
```

The server will start on port `8080`. You can access the UI at [http://localhost:8080](http://localhost:8080).
