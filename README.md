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

**Health check**
```bash
curl -s http://localhost:8081/health | jq
```

**Fetch quantum entropy** (256 bytes, pure quantum — sem mixing)
```bash
curl -s "http://localhost:8081/api/v1/quantum-random?count=256&pure=true" | jq
```

**Fetch quantum entropy** (256 bytes, mixed — NIST SP 800-90C)
```bash
curl -s "http://localhost:8081/api/v1/quantum-random?count=256&pure=false" | jq
```

---

### Key Manager (:8082)

**Health check**
```bash
curl -s http://localhost:8082/health | jq
```

**Gerar par de chaves RSA** (2048 ou 4096 bits)
```bash
curl -s -X POST http://localhost:8082/api/v1/keys \
  -H "Content-Type: application/json" \
  -d '{"alias": "minha-chave-1", "keySize": 2048}' | jq
```

**Listar todas as chaves**
```bash
curl -s http://localhost:8082/api/v1/keys | jq
```

**Exportar chave privada** (Key Wrapping — substitua 1 pelo ID da chave)
```bash
curl -s -X POST http://localhost:8082/api/v1/keys/1/export | jq
```

**Deletar uma chave** (substitua 1 pelo ID)
```bash
curl -s -X DELETE http://localhost:8082/api/v1/keys/1
```

**Deletar todas as chaves**
```bash
curl -s -X DELETE http://localhost:8082/api/v1/keys
```

**Status do pool de entropia**
```bash
curl -s http://localhost:8082/api/v1/quantum-entropy/status | jq
```

**Executar auditoria de entropia** (compara Quantum vs CSPRNG vs PRNG)
```bash
curl -s "http://localhost:8082/api/v1/quantum-entropy/audit?size=8192" | jq
```

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
