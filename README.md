# Quantum Entropy Service (Go Edition)

A microservices project rewritten in **Go** that fetches quantum random numbers from the LfD API, generates secure RSA keys, and uses **RabbitMQ** for event-driven async communication.

## Architecture

```
┌────────────┬────────────┐
│ Quantum    │    Key     │
│   API      │  Manager   │
│  (Gin)     │  (Gin)     │
│  :8081     │  :8082     │
├────────────┴────────────┤
│       RabbitMQ          │
│    :5672 / :15672       │
├─────────────────────────┤
│  In-Memory DB (GORM +   │
│  SQLite :memory:)       │
└─────────────────────────┘
```

## Tech Stack

| Technology | Purpose |
|------------|---------|
| **Go 1.23** | Main language |
| **Gin** | HTTP framework |
| **GORM + SQLite** | ORM + In-memory database |
| **RabbitMQ** | Event-driven messaging |
| **amqp091-go** | RabbitMQ Go client |
| **crypto/\*** (stdlib) | RSA, AES-256-GCM, SHA-256/512 |
| **Templ + HTMX** | Frontend (planned) |

## Quick Start

```bash
# Build and start all services
docker compose up --build -d

# Check status
docker compose ps

# View logs
docker compose logs -f quantum-api
docker compose logs -f quantum-keymanager

# RabbitMQ Management UI
open http://localhost:15672  # guest/guest

# Stop
docker compose down
```

## API Endpoints

### Quantum API (:8081)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Health check |
| GET | `/api/v1/quantum-random?count=256&pure=true` | Fetch quantum entropy |

### Key Manager (:8082)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Health check |
| POST | `/api/v1/keys` | Generate RSA key pair |
| GET | `/api/v1/keys` | List all keys |
| DELETE | `/api/v1/keys/:id` | Delete a key |
| DELETE | `/api/v1/keys` | Delete all keys |
| POST | `/api/v1/keys/:id/export` | Export private key (Key Wrapping) |
| GET | `/api/v1/quantum-entropy/status` | Entropy pool status |
| GET | `/api/v1/quantum-entropy/audit?size=8192` | Run entropy audit |

## RabbitMQ Events

| Exchange | Routing Key | Description |
|----------|-------------|-------------|
| `entropy.collected` | `entropy.new` | New entropy fetched from LfD |
| `entropy.collected` | `entropy.validated` | Entropy validated and saved |
| `key.events` | `key.created` | RSA key generated |
| `key.events` | `key.exported` | Key exported via Key Wrapping |
| `key.events` | `key.deleted` | Key deleted |
| `audit.requests` | `audit.start` | Audit requested |
| `audit.results` | `audit.complete` | Audit completed |

## Project Structure

```
cmd/
  quantum-api/main.go        # Quantum API entrypoint
  keymanager/main.go          # Key Manager entrypoint
internal/
  quantum/                    # Entropy collection (LfD)
  keymanager/                 # RSA key management
  audit/                      # Entropy Lab (validators)
  collector/                  # Entropy scheduler (goroutines)
  messaging/                  # RabbitMQ (publisher/consumer/events)
web/                          # Frontend (Templ + HTMX) [planned]
```

## 👨‍💻 Developed by
**Leporoni Tech Solutions**
📧 [leporonitech@gmail.com](mailto:leporonitech@gmail.com)
