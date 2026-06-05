# Análise Completa — `quantum-entropy-service-go`
> Última atualização: 2026-06-05

---

## 📌 Branches de Implementação

| Branch | Escopo | Status |
|--------|--------|--------|
| `main` | Scaffold inicial + ajustes de versão | ✅ Feito |
| `feat/keymanager-core` | `internal/keymanager/` completo (model, repository, service, handler) | ✅ Feito |
| `feat/entrypoints` | `cmd/quantum-api/main.go` + `cmd/keymanager/main.go` + fix `quantum/handler.go` | ✅ Feito |
| `feat/event-driven` | Eventos RabbitMQ `pool.low`/`pool.ok` + hysteresis no scheduler | ✅ Feito |
| `feat/web` | Frontend cyberpunk HTML + HTMX + `internal/ui/` | ✅ Feito |
| `feat/ui-rabbitmq-dashboard` | Tab RabbitMQ no frontend + fix system status server-side | ✅ Feito |

---

## 🗺️ O que o projeto faz

`quantum-entropy-service-go` é uma reescrita em Go do `quantum-entropy-service` (Java/Spring Boot), com a adição de mensageria assíncrona via RabbitMQ. O sistema coleta entropia quântica real da LfD Quantum API, armazena em pool, e usa essa entropia para gerar chaves RSA criptograficamente superiores às geradas com PRNG convencional.

### Fluxo principal

```
[LfD Quantum API] ──HTTP──► quantum-api (:8081)
  https://lfdr.de/qrng_api       │
  GET /qrng?length=256&format=HEX│
                                  │ scheduler (goroutine)
                                  │ coleta quando pool < 200
                                  │ para quando pool >= 1000
                                  │
                                  │ POST /api/v1/quantum-random
                                  ▼
                           keymanager (:8082)
                                  │
                            salva no SQLite (in-memory)
                                  │
                    ┌─────────────┴─────────────┐
                    ▼                           ▼
             POST /api/v1/keys          GET /api/v1/quantum-entropy/status
          (gera RSA com entropia)       (pool disponível)
          (consome 5 registros)
                    │
             POST /api/v1/keys/:id/export
          (AES-256-GCM unwrap)
          (consome 2 registros)
```

### Hysteresis event-driven (comunicação exclusiva via RabbitMQ)

```
keymanager: após gerar ou exportar chave, verifica pool
    pool < 200  → publica "entropy.pool.low"  → quantum-api chama TriggerRefill()
    pool >= 1000 → publica "entropy.pool.ok"  → quantum-api loga estado saudável

quantum-api scheduler:
    - tick normal:  verifica a cada 5s
    - refillChan:   dispara imediatamente ao receber pool.low
    - para ao atingir highWatermark (1000)
```

---

## 🧱 Arquitetura de Pacotes

```
quantum-entropy-service-go/
├── cmd/
│   ├── quantum-api/
│   │   └── main.go               # Entrypoint serviço 1 (porta 8081)
│   │                             # Bootstrap: LfD client, quantum service/handler,
│   │                             # RabbitMQ consumer (pool.low/pool.ok),
│   │                             # entropy scheduler (goroutine)
│   └── keymanager/
│       └── main.go               # Entrypoint serviço 2 (porta 8082)
│                                 # Bootstrap: SQLite/GORM, keymanager service/handler,
│                                 # audit service/handler, UI handler,
│                                 # RabbitMQ publisher, entropy scheduler
├── internal/
│   ├── quantum/
│   │   ├── client_lfd.go         # Cliente HTTP → LfD API (https://lfdr.de/qrng_api)
│   │   │                         # GET /qrng?length=N&format=HEX → decodifica hex → []byte
│   │   ├── service.go            # GetEntropyAsBase64(count, pure)
│   │   │                         # pure=true  → raw quantum bytes
│   │   │                         # pure=false → NIST SP 800-90C XOR mixing com crypto/rand
│   │   ├── mixing.go             # SHA-256 keystream XOR mixing + generateSystemEntropy
│   │   └── handler.go            # GET /api/v1/quantum-random?count=N&pure=bool
│   │
│   ├── keymanager/
│   │   ├── model.go              # GORM models:
│   │   │                         #   QuantumData (id, dataBase64, used, source)
│   │   │                         #   RsaKey (id, alias, keySize, publicKeyPEM,
│   │   │                         #           encryptedPrivatePEM, nonce)
│   │   ├── repository.go         # CRUD + queries especializadas:
│   │   │                         #   SaveEntropy, CountAllUnusedEntropy,
│   │   │                         #   FindAllUnusedBySource, ConsumeEntropy (transacional),
│   │   │                         #   SaveKey, FindAllKeys, FindKeyByID,
│   │   │                         #   DeleteKeyByID, DeleteAllKeys
│   │   ├── service.go            # GenerateKey(alias, keySize) → consome 5 registros,
│   │   │                         #   gera RSA com XOR reader quântico,
│   │   │                         #   wrap AES-256-GCM, publica pool event
│   │   │                         # ExportPrivateKey(id) → consome 2 registros,
│   │   │                         #   AES-256-GCM decrypt, publica pool event
│   │   │                         # checkAndPublishPoolEvent() → pool.low | pool.ok
│   │   └── handler.go            # POST   /api/v1/keys
│   │                             # GET    /api/v1/keys
│   │                             # DELETE /api/v1/keys/:id
│   │                             # DELETE /api/v1/keys
│   │                             # POST   /api/v1/keys/:id/export
│   │                             # GET    /api/v1/quantum-entropy/status
│   │
│   ├── messaging/
│   │   ├── connection.go         # RabbitMQ connection com retry exponencial + DLX
│   │   │                         # DeclareExchange, DeclareQueue, DeclareDeadLetterExchange
│   │   ├── publisher.go          # Publish(exchange, routingKey, event) → JSON + Persistent
│   │   ├── consumer.go           # Consume / ConsumeWithPrefetch (ack/nack manual)
│   │   │                         # SetupExchangesAndQueues() → declara toda a topologia
│   │   └── events.go             # Exchanges: entropy.collected, entropy.pool,
│   │                             #            key.events, audit.requests, audit.results
│   │                             # Routing keys: entropy.new, entropy.validated,
│   │                             #   key.created, key.exported, key.deleted,
│   │                             #   audit.start, audit.complete,
│   │                             #   entropy.pool.low, entropy.pool.ok
│   │                             # Payloads: EntropyNewEvent, EntropyValidatedEvent,
│   │                             #   KeyCreatedEvent, KeyExportedEvent, KeyDeletedEvent,
│   │                             #   AuditStartEvent, AuditCompleteEvent,
│   │                             #   PoolLowEvent, PoolOkEvent
│   │
│   ├── audit/
│   │   ├── service.go            # RunFullAudit(size) → compara 3 fontes:
│   │   │                         #   Quantum (LFD), CSPRNG (crypto/rand), PRNG (math/rand)
│   │   ├── handler.go            # GET /api/v1/quantum-entropy/audit?size=N
│   │   └── validators/
│   │       └── validators.go     # CalculateShannonEntropy, CalculateChiSquare,
│   │                             # EstimatePiMonteCarlo, CalculateCompressionRatio,
│   │                             # CountRepetitions
│   │
│   ├── collector/
│   │   └── scheduler.go          # Scheduler: goroutine com ticker de 5s
│   │                             # + refillChan (canal de sinal imediato via pool.low)
│   │                             # lowWatermark=200, highWatermark=1000
│   │                             # fetchAndSave() → GET quantum-api → salva QuantumData
│   │
│   └── ui/
│       └── handler.go            # Fragmentos HTMX servidos pelo Gin:
│                                 # GET  /ui/pool-status     → barra de entropia
│                                 # GET  /ui/system-status   → health check server-side
│                                 #                            (quantum-api + RabbitMQ)
│                                 # GET  /ui/rabbitmq-queues → proxy Management API
│                                 # GET  /ui/keys            → tabela de chaves
│                                 # POST /ui/keys            → gera chave + retorna tabela
│                                 # DELETE /ui/keys          → deleta todas
│                                 # DELETE /ui/keys/:id      → deleta uma
│                                 # POST /ui/keys/:id/export → exibe PEM inline
│                                 # GET  /ui/audit           → resultados de auditoria
│
├── web/
│   ├── static/
│   │   ├── index.html            # SPA cyberpunk: tabs Dashboard, Key Vault,
│   │   │                         # Entropy Lab, RabbitMQ. HTMX para todas as
│   │   │                         # interações, zero JS customizado para lógica de negócio
│   │   └── css/
│   │       └── cyberpunk.css     # Tema neon cyan/magenta, glassmorphism,
│   │                             # grid animado, badges, tabela, forms, toast
│   └── templates/
│       └── doc.go                # Placeholder do pacote templates
│
├── docker-compose.yml            # quantum-api:8081, keymanager:8082, rabbitmq:5672/15672
├── Dockerfile.api                # golang:1.25-alpine builder + alpine:3.19 runtime
├── Dockerfile.keymanager         # golang:1.25-alpine builder + alpine:3.19 runtime
│                                 # WORKDIR /app + copia web/static para o runtime
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
| Entropia externa | LfD Quantum API — `https://lfdr.de/qrng_api` |
| Validação NIST | SP 800-90B: Shannon, Chi-Square, Monte Carlo Pi, Compression Ratio, Repetitions |
| Frontend | HTML + HTMX 1.9 (sem framework JS) |
| Containerização | Docker + Docker Compose |

---

## 🔐 Decisões de Design

### AES-256-GCM (melhoria sobre o Java)
O projeto Java usava AES/ECB (sem autenticação). A versão Go usa **AES-256-GCM** — modo autenticado que detecta adulteração da chave privada armazenada. O nonce é gerado aleatoriamente por operação e armazenado junto ao ciphertext.

### Full event-driven (sem HTTP entre serviços)
No Java, o keymanager fazia HTTP polling no quantum-api. Na versão Go, **toda comunicação entre serviços é via RabbitMQ**. Isso elimina acoplamento temporal e permite escalar os serviços independentemente.

### Hysteresis via eventos
| Evento | Publicado por | Consumido por | Trigger |
|--------|--------------|---------------|---------|
| `entropy.pool.low` | keymanager | quantum-api | Pool < 200 registros após gerar/exportar chave |
| `entropy.pool.ok` | keymanager | quantum-api | Pool >= 1000 registros após gerar/exportar chave |

### Pool vazio → 503 + evento
Quando `POST /keys` é chamado com pool vazio, retorna `503 Service Unavailable` **e** publica `entropy.pool.low` para forçar recuperação automática.

### System Status server-side
O health check dos serviços no frontend é feito pelo **backend Go** (não pelo browser) via `/ui/system-status`. Isso resolve o problema de DNS — o browser não consegue resolver `quantum-api` ou `rabbitmq` pois esses nomes só existem na rede interna Docker.

### Frontend HTMX sem JS
O frontend não possui JavaScript de lógica de negócio. Toda interação (gerar chave, deletar, exportar, auditoria, refresh de pool) é feita com atributos HTMX que trocam fragmentos HTML retornados pelo servidor Gin.

---

## 📋 Custos de Entropia por Operação

| Operação | Registros consumidos |
|----------|---------------------|
| `POST /keys` (gerar RSA) | 5 registros LFD (256 bytes cada = 1.25 KB) |
| `POST /keys/:id/export` | 2 registros LFD (512 bytes) |

---

## ✅ Estado atual — tudo implementado

- `internal/quantum/` — completo e funcional
- `internal/keymanager/` — completo e funcional
- `internal/messaging/` — completo e funcional
- `internal/audit/` — completo e funcional
- `internal/collector/` — completo com hysteresis e TriggerRefill
- `internal/ui/` — completo com fragmentos HTMX e proxy RabbitMQ
- `cmd/quantum-api/main.go` — completo
- `cmd/keymanager/main.go` — completo
- `web/static/` — frontend cyberpunk completo
- Infra Docker — completa (web/static copiado para runtime)

---

## 🔗 Variáveis de Ambiente

| Serviço | Variável | Padrão |
|---------|----------|--------|
| quantum-api | `PORT` | `8081` |
| quantum-api | `LFD_API_URL` | `https://lfdr.de/qrng_api` |
| quantum-api | `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` |
| quantum-api | `API_BASE_URL` | `http://localhost:8081` |
| keymanager | `PORT` | `8082` |
| keymanager | `MASTER_KEY_SECRET` | *(obrigatório)* |
| keymanager | `API_BASE_URL` | `http://quantum-api:8081` |
| keymanager | `RABBITMQ_URL` | `amqp://guest:guest@rabbitmq:5672/` |
| keymanager | `RABBITMQ_MGMT_HOST` | `rabbitmq:15672` |

## 🌐 Endpoints de Acesso

| Serviço | URL |
|---------|-----|
| Frontend UI | http://localhost:8082 |
| Quantum API | http://localhost:8081 |
| Key Manager API | http://localhost:8082/api/v1 |
| RabbitMQ Management | http://localhost:15672 (guest/guest) |
