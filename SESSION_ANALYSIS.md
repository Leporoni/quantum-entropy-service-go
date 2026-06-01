# Análise Completa — `quantum-entropy-service-go`
> Última atualização: 2026-05-31

---

## 📌 Branches de Implementação

| Branch | Escopo | Status |
|--------|--------|--------|
| `main` | Scaffold inicial + ajustes de versão | ✅ Feito |
| `feat/keymanager-core` | `internal/keymanager/` completo (model, repository, service, handler) | ✅ Feito |
| `feat/entrypoints` | `cmd/quantum-api/main.go` + `cmd/keymanager/main.go` | ✅ Feito |
| `feat/event-driven` | Eventos RabbitMQ `pool.low`/`pool.ok` + fix `quantum/handler.go` | 🔲 Pendente |
| `feat/web` | Frontend Templ + HTMX (opcional) | 🔲 Opcional |

---

## 🗺️ O que o projeto faz

`quantum-entropy-service-go` é uma reescrita em Go do `quantum-entropy-service` (Java/Spring Boot), com a adição de mensageria assíncrona via RabbitMQ. O sistema coleta entropia quântica real de APIs externas, armazena em pool, e usa essa entropia para gerar chaves RSA criptograficamente superiores às geradas com PRNG convencional.

### Fluxo principal

```
[LfD Quantum API] ──HTTP──► quantum-api (scheduler)
                                  │
                                  │ publica "entropy.collected"
                                  ▼
                            [RabbitMQ]
                                  │
                                  │ consome "entropy.collected"
                                  ▼
                           keymanager (consumer)
                                  │
                            valida NIST SP 800-90B
                                  │
                            salva no SQLite (in-memory)
                                  │
                    ┌─────────────┴─────────────┐
                    ▼                           ▼
             POST /keys                  GET /quantum-entropy/status
          (gera RSA com entropia)        (pool disponível)
```

### Hysteresis event-driven (sem HTTP entre serviços)

```
keymanager detecta pool < 200
    → publica "entropy.pool.low" no RabbitMQ

quantum-api consome "entropy.pool.low"
    → acelera scheduler (modo refill)

keymanager detecta pool >= 1000
    → publica "entropy.pool.ok" no RabbitMQ

quantum-api consome "entropy.pool.ok"
    → scheduler volta ao ritmo normal
```

---

## 🧱 Arquitetura de Pacotes

```
quantum-entropy-service-go/
├── cmd/
│   ├── quantum-api/main.go       # Entrypoint serviço 1 (porta 8081)
│   └── keymanager/main.go        # Entrypoint serviço 2 (porta 8082)
├── internal/
│   ├── quantum/
│   │   ├── client_lfd.go         # Cliente HTTP → LfD Quantum API
│   │   ├── service.go            # Lógica de entropia (pure/mixed, NIST SP 800-90C)
│   │   ├── mixing.go             # SHA-256 keystream XOR mixing com crypto/rand
│   │   └── handler.go            # GET /api/v1/quantum-random ⚠️ fix count pendente
│   ├── keymanager/               # 🔲 AUSENTE — próxima branch
│   │   ├── model.go              # GORM models: QuantumData, RsaKey
│   │   ├── repository.go         # CRUD + queries especializadas
│   │   ├── service.go            # RSA generation + AES-256-GCM key wrapping
│   │   └── handler.go            # REST endpoints /keys + /quantum-entropy/*
│   ├── messaging/
│   │   ├── connection.go         # RabbitMQ connection com retry + DLX
│   │   ├── publisher.go          # PublishWithContext
│   │   ├── consumer.go           # Consumer com ack/nack manual + prefetch
│   │   └── events.go             # Tipos de eventos + constantes exchange/routing key
│   ├── audit/
│   │   ├── service.go            # Auditoria multi-source (depende de keymanager.Repository)
│   │   ├── handler.go            # GET /api/v1/quantum-entropy/audit
│   │   └── validators/
│   │       └── validators.go     # Shannon, Chi-Square, Monte Carlo Pi, Compression, Repetitions
│   └── collector/
│       └── scheduler.go          # Goroutine hysteresis (depende de keymanager.Repository)
├── web/
│   ├── templates/                # Templ templates (opcional)
│   └── static/                   # CSS/JS estáticos
├── docker-compose.yml            # quantum-api:8081, keymanager:8082, rabbitmq:5672
├── Dockerfile.api
├── Dockerfile.keymanager
├── go.mod
└── go.sum
```

---

## 🔧 Stack Tecnológica

| Componente | Tecnologia |
|------------|------------|
| Linguagem | Go 1.25 |
| HTTP Framework | Gin v1.12 |
| ORM | GORM v1.25 + SQLite driver |
| Banco de dados | SQLite in-memory (keymanager) |
| Mensageria | RabbitMQ via `amqp091-go` v1.10 |
| Criptografia | `crypto/rsa`, `crypto/aes` (AES-256-GCM), `crypto/rand`, SHA-256/SHA-512 |
| Entropia externa | LfD Quantum API (HTTP, retorna hex) |
| Validação NIST | SP 800-90B: Shannon, Chi-Square, Monte Carlo Pi, Compression Ratio, Repetitions |
| Containerização | Docker + Docker Compose |
| Frontend (opcional) | Templ + HTMX |

---

## 🔐 Decisões de Design

### AES-256-GCM (melhoria sobre o Java)
O projeto Java usava AES/ECB (sem autenticação). A versão Go usa **AES-256-GCM** — modo autenticado que detecta adulteração da chave privada armazenada.

### Full event-driven (sem HTTP entre serviços)
No Java, o keymanager fazia HTTP polling no quantum-api. Na versão Go, **toda comunicação entre serviços é via RabbitMQ**. Isso elimina acoplamento temporal e permite escalar os serviços independentemente.

### Hysteresis via eventos
| Evento | Publicado por | Consumido por | Trigger |
|--------|--------------|---------------|---------|
| `entropy.collected` | quantum-api | keymanager | A cada coleta bem-sucedida |
| `entropy.pool.low` | keymanager | quantum-api | Pool < 200 registros |
| `entropy.pool.ok` | keymanager | quantum-api | Pool >= 1000 registros |

### Pool vazio → 503 + evento
Quando `POST /keys` é chamado com pool vazio, retorna `503 Service Unavailable` **e** publica `entropy.pool.low` para forçar recuperação automática.

---

## 📋 Custos de Entropia por Operação

| Operação | Registros consumidos |
|----------|---------------------|
| `POST /keys` (gerar RSA) | 5 registros LFD |
| `POST /keys/:id/export` | 2 registros LFD |

---

## ✅ O que já está feito (main)

- `internal/quantum/` — completo e funcional
- `internal/messaging/` — completo e funcional
- `internal/audit/validators/` — completo
- `internal/audit/service.go` + `handler.go` — parcial (depende de keymanager.Repository)
- `internal/collector/scheduler.go` — parcial (depende de keymanager.Repository)
- Infra Docker — completa

## ❌ O que está faltando

- `internal/keymanager/` — pacote inteiro ausente (**bloqueador crítico**)
- `cmd/quantum-api/main.go` — ausente
- `cmd/keymanager/main.go` — ausente
- Fix `quantum/handler.go` — TODO de parse do `count` com `strconv.Atoi`
- Eventos `pool.low` / `pool.ok` no keymanager e consumer no quantum-api

---

## 🔗 Variáveis de Ambiente

| Serviço | Variável | Padrão |
|---------|----------|--------|
| quantum-api | `PORT` | `8081` |
| quantum-api | `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` |
| keymanager | `PORT` | `8082` |
| keymanager | `MASTER_KEY_SECRET` | *(obrigatório)* |
| keymanager | `API_BASE_URL` | `http://quantum-api:8081` |
| keymanager | `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` |
