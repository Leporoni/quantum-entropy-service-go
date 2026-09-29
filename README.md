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
│  SQLite shared-cache)   │
└─────────────────────────┘
```

The key manager opens SQLite with the DSN `file::memory:?cache=shared`
(`cmd/keymanager/main.go:25`). It is a single shared in-memory database for the
whole process, which is exactly what lets the scheduler, the handlers and the
repository see the same pool. The trade-off is that shared-cache mode raises the
lock granularity to the **whole table**, so concurrent writers race into
`database is locked`. That constraint — not preference — is what forces the
collector's **single-writer** discipline: parallel workers fetch over HTTP, but
only one goroutine ever calls `SaveEntropy`.

## Tech Stack

| Technology | Purpose |
|------------|---------|
| **Go 1.25** | Main language |
| **Gin** | HTTP framework |
| **GORM + SQLite** | ORM + in-memory database (`file::memory:?cache=shared`) |
| **RabbitMQ** | Event-driven messaging |
| **amqp091-go** | RabbitMQ Go client |
| **crypto/\*** (stdlib) | RSA, AES-256-GCM, SHA-256/512 |
| **HTMX** | Frontend (zero-JS interactions) |
| **sync / sync/atomic** | Concurrency: `WaitGroup`, `Mutex`, `Once`, `atomic.Int64`, channels |

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

> **Using the standalone Docker Compose v1?** Common on WSL2 installs that ship the
> legacy binary. Replace `docker compose` with `docker-compose` (hyphen) in every
> command above. Note that `docker-compose logs -f` can crash with
> `KeyError: 'id'` on some v1 builds — in that case fall back to
> `docker logs -f quantum-keymanager` or `docker logs --tail=100 quantum-keymanager`.

## Frontend — Cyberpunk UI

Acesse o frontend em **http://localhost:8082** após subir os containers.

Construído com HTML + HTMX e tema cyberpunk (neon cyan/magenta, glassmorphism, grid animado). Todas as interações são feitas sem JavaScript customizado — o HTMX troca fragmentos HTML diretamente com o servidor.

### Dashboard
Mostra o status do pool de entropia em tempo real com barra animada (atualiza a cada 3s via HTMX polling) e o status dos serviços.

### Key Vault
Interface completa para gerenciamento de chaves RSA:
- Gerar novo par de chaves (2048 ou 4096 bits) com feedback inline
- Listar todas as chaves em tabela
- Exportar chave privada (AES-256-GCM unwrapping) com botão de cópia
- Deletar chave individual ou todas

### Entropy Lab
Roda **4 suítes** de auditoria (`internal/audit/suites.go:63-97`), cada uma comparando a
entropia quântica (LfD) contra o CSPRNG (`crypto/rand`) e um PRNG determinístico
(`math/rand`). Dentro de cada suíte as métricas rodam **em paralelo** (fan-in) e um
collector reordena os resultados por índice, então a ordem da tabela é sempre a mesma
independentemente de quem terminou primeiro.

| Suíte | Mínimo recomendado | Métricas |
|-------|--------------------|----------|
| `basic` | 1 KB | Shannon Entropy, Chi-Square, Pi (Monte Carlo), Compression Ratio, Repetitions |
| `min-entropy` | 1 MB | Min-entropy 8-bit (MCV), min-entropy bit-level, most common value, distinct byte values |
| `nist` | 125 KB (1 Mbit) | Monobit, Block Frequency, Runs, Longest Run, Approximate Entropy, Serial, Cumulative Sums (fwd + rev) |
| `structure` | 64 KB | Bit bias, autocorrelation (lags 1–16), runs z-score, serial correlation |

Rota: **`GET /ui/lab?suite=<id>&size=<bytes>&seed=<int>`**

- `suite` ∈ `basic` | `min-entropy` | `nist` | `structure` (default `basic`)
- `size` em bytes, default `8192`; o seletor da UI oferece de 1 KB a 256 KB
- `seed` semeia **apenas** a fonte PRNG (`math/rand`), tornando a coluna "Java Random
  (LCRNG)" reprodutível entre execuções. `seed=0` (ou ausente) usa `DefaultPRNGSeed`
  (`12345`). As fontes quântica e CSPRNG são sempre aleatórias de verdade — só o PRNG
  é determinístico. A UI não expõe o campo, então ele se usa via URL.
  *(Os rótulos das três fontes ainda dizem "Java" — vestígio do port; a implementação
  é `crypto/rand` e `math/rand` do Go. Ver Limitações.)*
- Quando a amostra fica abaixo do mínimo da suíte, o resultado é marcado como
  **"indicative"** em vez de veredito formal pass/fail.

## API Endpoints

### Quantum API (:8081)

**Health check**
```bash
curl -s http://localhost:8081/health | jq
```

**Fetch quantum entropy** (1024 bytes — o máximo do endpoint — pure quantum, sem mixing)
```bash
curl -s "http://localhost:8081/api/v1/quantum-random?count=1024&pure=true" | jq
```

**Fetch quantum entropy** (1024 bytes, mixed — NIST SP 800-90C)
```bash
curl -s "http://localhost:8081/api/v1/quantum-random?count=1024&pure=false" | jq
```

> `count` aceita de 1 a `quantum.MaxCount = 1024` (`internal/quantum/service.go:11`);
> o default é 128. Acima disso o endpoint responde **400**.

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

## Collector — pool de entropia

O pool é mantido por um scheduler em background (`internal/collector/scheduler.go`).
Ele é o **único** componente que escreve entropia vinda da rede.

- **Loop:** um `time.Ticker` de 5 s chama `collectEntropy()`; o mesmo loop também
  aceita `TriggerRefill()` — um sinal disparado pelo key manager quando o pool cai
  abaixo do mínimo, para não esperar o próximo tick.
- **Hysteresis com dois watermarks:** refill começa quando o pool está **abaixo de 200**
  registros e para quando atinge **1000** (`lowWatermark` / `highWatermark`).
- **Registro = 256 B.** O tamanho do registro é intencional: os watermarks e o consumo
  por operação de chave (`entropyPerKey = 5`, `entropyPerExport = 2`) contam
  **registros**, não bytes.
- **Split do chunk:** cada fetch pede os 1024 B máximos do endpoint e `saveRecords`
  fatia em **4 registros de 256 B** (`recordsPerFetch = entropyChunkBytes / entropyRecordBytes`).
- **Fan-out com 4 workers:** `runRefillBatch` distribui os fetches por 4 goroutines via
  um canal `jobs` **não-bufferizado**. Os workers só fazem HTTP; quem grava é o
  collector. Um `atomic.Int64` (`failCount`) funciona como circuit breaker e aborta o
  batch em `maxFailures = 10`.
- **`pool.ok` só ao atingir o high watermark:** no fim do refill, e **somente** se a
  contagem chegou a 1000, o scheduler publica `entropy.pool.ok`. Um refill que desistiu
  no meio não anuncia um estado que não alcançou.

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
| `entropy.pool` | `entropy.pool.low` | Pool below low watermark (200) — published by keymanager after key op |
| `entropy.pool` | `entropy.pool.ok` | Pool reached high watermark (1000) — published by scheduler after refill |

### Queues and bindings

The topology is declared by `declareTopology()`
(`internal/messaging/consumer.go:79-122`) on the connection's own channel, under
the same mutex `Channel()` holds. It is **not** memoised by a `sync.Once`: the
`topologyDeclared` flag is set only after the declaration returns `nil`, so a
failure leaves the topology undeclared and the next call retries. Every queue is
durable and bound to exactly one exchange + routing key:

| Queue | Exchange | Routing Key |
|-------|----------|-------------|
| `q.entropy.new` | `entropy.collected` | `entropy.new` |
| `q.entropy.validated` | `entropy.collected` | `entropy.validated` |
| `q.key.created` | `key.events` | `key.created` |
| `q.key.exported` | `key.events` | `key.exported` |
| `q.key.deleted` | `key.events` | `key.deleted` |
| `q.audit.start` | `audit.requests` | `audit.start` |
| `q.audit.complete` | `audit.results` | `audit.complete` |
| `q.pool.low` | `entropy.pool` | `entropy.pool.low` |
| `q.pool.ok` | `entropy.pool` | `entropy.pool.ok` |

Plus the dead-letter exchange **`dlx.quantum`** (fanout, durable). **It is
declared but not yet bound to anything** — no queue passes an
`x-dead-letter-exchange` argument, and `Consume` requeues with
`msg.Nack(false, true)`, which never routes to a DLX. Wiring the dead-letter path
is known, unimplemented backlog. The **RabbitMQ** tab in the UI reads the
exchanges and queues live from the Management API via `GET /ui/rabbitmq-queues`.

> **Connection is lazy.** `NewConnection` does not dial
> (`internal/messaging/connection.go:74-76`); the first `Channel()` call connects,
> under a `sync.Mutex` (`internal/messaging/connection.go:94-107`).
>
> Every failure mode on that path is retryable — none of them is permanent:
>
> - **A failed dial** is remembered for `reconnectDelay` (5 s) so concurrent
>   publishers do not each re-dial on every event while the broker is down. The
>   cached error still wraps the root cause with `%w`, so `errors.Is` reaches it.
> - **A failed topology declaration** is *not* sticky: `topologyDeclared` stays
>   `false` and the next `Channel()` retries.
> - **A reconnect** (the session reports itself closed) dials again, closes the
>   old session, and resets `topologyDeclared`, so the fresh channel gets its own
>   exchanges and queues.
>
> So a broker that is down at boot no longer disables messaging for the process
> lifetime — but the recovery is also real, not just the boot path.

## Environment Variables

| Variable | Service | Default | Required |
|----------|---------|---------|----------|
| `PORT` | both | `8081` (api) / `8082` (keymanager) | no |
| `MASTER_KEY_SECRET` | keymanager | — | **yes** |
| `API_BASE_URL` | keymanager | `http://quantum-api:8081` | no |
| `RABBITMQ_URL` | keymanager | `amqp://guest:guest@rabbitmq:5672/` | no |
| `PANIC_DEBUG` | keymanager | *(unset)* | no |
| `RABBITMQ_MGMT_HOST` | keymanager (UI) | `rabbitmq:15672` | no |

- **`MASTER_KEY_SECRET`** — the raw secret is **SHA-256 hashed** to derive the 32-byte
  **AES-256** key that wraps the RSA private keys
  (`internal/keymanager/service.go:73-79`, derived once via `sync.Once`).
  The process refuses to start without it.
- **`PANIC_DEBUG=true`** registers the canary route `GET /debug/panic`, which panics on
  purpose to exercise the custom recovery middleware. Never enable it in production.

## Concurrency Architecture

The most interesting part of the codebase — six concurrency patterns, each with a
concrete reason to exist.

**Fan-out — collector refill** (`internal/collector/scheduler.go:146-204`)
4 HTTP workers read from an **unbuffered** `jobs` channel (one job delivered exactly
once; the producer can abort early), push into a buffered `results` channel, and a
dedicated goroutine does `wg.Wait()` + `close(results)`. The collector ranges over
`results` and is the **single writer** to SQLite — parallel saves would hit
`database is locked`. `atomic.Int64` (`failCount`) is the shared circuit breaker.

**Fan-in — Entropy Lab** (`internal/audit/suites.go:259-286`)
Each metric closure runs in its own goroutine and sends
`indexedMetrics{idx, items}` over a buffered channel; the collector writes into
`groups[im.idx]`, restoring the input order regardless of completion order. A
separate goroutine closes the channel after `wg.Wait()`. Each suite fans out 4–7
closures at a time (5 for `basic`, 4 for `min-entropy`, 7 for `nist`, 4 for `structure`).

**`sync.Mutex` — `xorReader`** (`internal/keymanager/service.go:300-329`)
The quantum seed reader XORs the CSPRNG output with quantum bytes. The lock covers
**only the XOR loop**: `rand.Reader.Read` stays outside so the CSPRNG call still runs
fully parallel, and the empty-seed path returns before ever taking the lock. There is
no race today (one reader per `GenerateKey`, one goroutine); the lock hardens the
primitive because CIRCL will reuse this reader as the ML-KEM/ML-DSA seed source, whose
generation may consume it in parallel (`docs/CIRCL_INTEGRATION_PLAN.md:95,163`).

**`sync.Once` — AES master key** (`internal/keymanager/service.go:34-46,73-79`)
The 32-byte AES-256 key is `SHA-256(MASTER_KEY_SECRET)`. It used to be re-hashed on
every `aesGCMEncrypt`/`aesGCMDecrypt` call; `keyOnce` derives it once and reuses it.
This is the **only** legitimate `sync.Once` in the codebase, and the reason is
precisely why `sync.Once` was the wrong tool for the AMQP topology: deriving a key is
a pure function of a secret that is immutable for the process lifetime, so it cannot
fail transiently and there is nothing to retry.

**`sync.Mutex` — AMQP connection** (`internal/messaging/connection.go:46-75,114-127`)
The same reasoning runs the other way. Dial and `declareTopology` both fail
*transiently* — a broker restarting mid-declaration, or a queue whose arguments
diverged from the broker's view (`PRECONDITION_FAILED`) — and a redial or a channel
reopen hands back a channel with no exchanges at all. A `sync.Once` cannot express
any of those, and burning its one firing on the first error poisons messaging for the
process lifetime: `topologyErr` stuck forever, and then, after the next reconnect, a
brand-new channel with no topology, so every `Publish` after a broker restart returns
`404 NOT_FOUND`. Hence a plain mutex plus a `topologyDeclared` bool: a failed
declaration stays retryable, a reconnect resets the flag, and a failed dial is cached
for 5 s **with its root cause wrapped in `%w`** rather than replaced by a generic
"unavailable" message.

Liveness is checked on the **channel**, not just the session
(`connectLocked`, `internal/messaging/connection.go:153-195`). The broker kills the
channel — not the connection — on a `PRECONDITION_FAILED`, which is precisely the
transient failure above; the session stays healthy, so a session-only check kept
serving the dead channel and every `Publish` after it returned `amqp.ErrClosed`
forever, with no redial, no re-declared topology and no error from `Channel()` to show
for it. The fix reopens the channel on the live session — one round trip, instead of
redoing TCP + AMQP handshake + auth on exactly the path a restart storm hammers — and
the new channel declares its own topology. `Close()` returns `error` and releases the
mutex before the close round trip, which has no timeout of its own; holding the lock
across it would park every `Publish`, including the ones serving an HTTP request.

The negative cache exists because `Channel()` genuinely has concurrent callers —
and they are **not** the collector's 4 refill workers. Those only do HTTP
(`internal/collector/scheduler.go:155-174`); `publishEntropyEvents` runs in the
single-writer loop, so the collector contributes one publisher, not four. The real
concurrency is the **Gin HTTP handlers** (one goroutine per request — key generate,
key export, audit run) plus the audit path, each publishing on its own goroutine.
With a broker down, that is one `Channel()` per in-flight request.

**`chan struct{}` as a semaphore — LfD client** (`internal/quantum/client_lfd.go:20,47-48`)
`sem chan struct{}` with `maxConcurrentLfd = 4` caps simultaneous requests to the
physical quantum device. A buffered channel used as a counting semaphore — a global
ceiling, not just a per-scheduler one.

## Tests

```bash
# Everything
go test ./...

# With the race detector (the meaningful run for this codebase)
go test -race ./...

# Soak the concurrency-heavy packages to catch flakiness
go test -race -count=30 ./internal/messaging/ ./internal/keymanager/ ./internal/collector/
```

There are **no external test dependencies** (no testify, no mocks framework) — that is
deliberate. Fakes are hand-written against the injected interfaces (`EntropyStore`,
`KeyStore`, `EventPublisher`) and against the `amqpSession` seam in
`internal/messaging`, so tests need no broker, no database and no network.

`amqp091-go` exposes no way to build a `*amqp.Connection` outside `Dial`/`DialConfig`,
which would otherwise make the connect, rollback and reconnect branches unreachable.
The `amqpSession` interface (`internal/messaging/connection.go:38-42`) is the seam that
makes them testable; the `topology` field is the matching seam for the declarations.

**What is still not covered, and why.** The AMQP round trips themselves —
`ExchangeDeclare`, `QueueDeclare`, `QueueBind`, `Consume`, the success path of
`Publish`, `Qos` and `Connection.Close()` — are **not covered by anything**. There is
no integration suite, no build tag, and no Docker target that exercises them: the only
thing `docker-compose.yml` does for RabbitMQ is a `rabbitmq-diagnostics -q ping`
healthcheck. `*amqp.Channel` is a concrete type and the `amqpSession` seam stops at the
connection, so `declareTopology`, `DeclareExchange`, `DeclareQueue` and
`DeclareDeadLetterExchange` sit at 0% and the success paths of `Publish`/`Consume`/`Qos`
are unreachable from a unit test. A shallow `amqpChannel` interface covering those calls
would fix it; it was **deferred on purpose**, and this paragraph is the record of that,
not a promise that a Docker environment picks it up.

At 0%: `declareTopology`, `DeclareExchange`, `DeclareQueue`,
`DeclareDeadLetterExchange`. Opened but never reached: the success path of `Publish`,
of `Consume`, of `Qos`, and the close round trip inside `Close()` (which sits at 87.5% —
it is the part that closes things that has no test).

`internal/messaging` therefore sits at **66.9%** statement coverage — up from **0%**,
since at `89349ab` the package had no test file at all.

## Troubleshooting — WSL2 / Docker DNS

**Symptom:** the entropy pool stays stuck at **0** and the API returns 500 in ~30 ms with:

```
dial tcp: lookup lfdr.de on 127.0.0.11:53: no such host
```

**Cause:** on WSL2 the host's `/etc/resolv.conf` points at `10.255.255.254`, which is
unreachable from the container's NAT. Docker's embedded resolver (`127.0.0.11`)
forwards to that nameserver and fails. Note the `network: host` in `docker-compose.yml`
sits inside `build:`, so it only applies to the **build**, not to runtime.

**Fix already applied** — public resolvers are pinned on the `quantum-api` service:

```yaml
dns:
  - 8.8.8.8
  - 1.1.1.1
```

Only `quantum-api` needs it: the key manager resolves `quantum-api` through Docker's
embedded DNS and never talks to the internet.

**Diagnose:**

```bash
# 1. Is name resolution working inside the container?
docker exec quantum-api nslookup lfdr.de

# 2. Does the end-to-end path work? Expect HTTP/1.1 200 (run this from the HOST)
curl -i "http://localhost:8081/api/v1/quantum-random?count=1024&pure=true"

# 3. Confirm the DNS pin is applied
docker inspect quantum-api --format '{{json .HostConfig.Dns}}'
#   → ["8.8.8.8","1.1.1.1"]
```

> ⚠️ The final image is `alpine:3.19` and ships **no `curl`** — only busybox applets.
> Use `nslookup` for step 1, and run the `curl` in step 2 **from the host**, not inside
> the container. Also note that the pin from step 3 does **not** show up in
> `/etc/resolv.conf` inside the container, which still reads `nameserver 127.0.0.11`
> (Docker injects the upstreams at the daemon level). Seeing `127.0.0.11` there is
> expected and is **not** a sign that the fix was lost.

## Empty-pool contract

An empty pool is a **legitimate state**, not an error, and the API reflects that
deliberately:

| Call | Empty pool |
|------|-----------|
| `POST /ui/keys` (htmx) | **503** with an inline error fragment |
| `POST /api/v1/keys` | **503** `{"error":"pool exhausted: insufficient entropy in pool"}` |
| `GET /api/v1/quantum-entropy/audit` | **200** `{"sampleSize":0,"results":[]}` |
| `GET /ui/lab` / `GET /ui/audit` | 200 with a "wait for pool to fill" notice |
| `audit.start` / `audit.complete` events | **none published** |

The audit acquires the quantum sample **before** publishing anything, so with an empty
pool the suite never runs and **no** `audit.start`/`audit.complete` is emitted. This
was a real bug: any poll of `/ui/audit` on an empty pool used to produce phantom events.
That legacy route is still registered (`internal/ui/handler.go:53`) and returns an empty
payload on an empty pool; the bundled UI no longer polls it — the button-driven Entropy
Lab at `/ui/lab` replaced it.

Note the asymmetry in the pool events: **`pool.low` only fires on a key operation**
(generate/export leaving the pool under 200) — it is not a periodic watchdog.
**`pool.ok` only fires when a refill actually completes** and reaches 1000.

## Project Structure

```
cmd/
  quantum-api/main.go            # Quantum API entrypoint (graceful shutdown + WaitGroup)
  keymanager/main.go             # Key Manager entrypoint (lazy RabbitMQ, OnPoolLow wiring)
internal/
  audit/                         # Entropy Lab: service, 4 suites, HTTP handler
  audit/validators/              # Shannon, Chi-Square, Monte Carlo, min-entropy, NIST SP 800-22 subset
  collector/                     # Entropy scheduler (fan-out refill pool)
  keymanager/                    # RSA CRUD + AES-256-GCM wrap + quantum-seeded xorReader
  messaging/                     # RabbitMQ: lazy connection, publisher, consumer, topology, events
  middleware/                     # Custom recovery middleware (defer/recover + stack)
  quantum/                       # LfD client (semaphore) + NIST SP 800-90C mixing
  ui/                            # HTMX fragment handlers
web/
  static/index.html              # Cyberpunk single-page frontend
  static/css/cyberpunk.css       # Neon theme
  static/js/                     # (reserved)
  templates/                     # (reserved)
docs/
  PROJECT_DOCUMENTATION.md       # Deep technical reference
  PROGRESS_STATUS.md             # Branch-by-branch progress log
  CIRCL_INTEGRATION_PLAN.md      # Post-quantum roadmap (ML-KEM / ML-DSA)
  entropy-lab-suites-analysis.md
Dockerfile.api                   # Multi-stage build → alpine:3.19
Dockerfile.keymanager            # Multi-stage build → alpine:3.19
docker-compose.yml               # quantum-api + keymanager + rabbitmq
```

## Limitations

This is a **research / demonstration project**, not a production service.

- **The database is in-memory.** `file::memory:?cache=shared` means **all data is lost
  on restart** — keys, entropy pool, everything. There is no persistence layer.
- **Credentials are demo-grade.** RabbitMQ uses `guest/guest` and
  `MASTER_KEY_SECRET=super_secret_master_key_change_me_in_prod` is committed in
  `docker-compose.yml`. Both must be replaced before any real use.
- **Hard dependency on `lfdr.de`.** If the external LfD API is unreachable, entropy
  cannot be collected and key generation returns 503. There is no fallback source.
- **`entropy.validated` reports `poolSize` as the record ID**, not the pool size
  (`internal/collector/scheduler.go:288`). Known cosmetic bug; the field is misleading.
- **The Entropy Lab still labels its sources "Java SecureRandom (CSPRNG)" and "Java
  Random (LCRNG)"** (`internal/audit/suites.go:142,146`) — leftovers from the Java
  edition. The code underneath is Go's `crypto/rand` and `math/rand`.
- **`PANIC_DEBUG=true` is set in `docker-compose.yml`**, which exposes the canary
  route `GET /debug/panic`. Fine for the demo, must be removed for any real use.
- **Compression Ratio uses gzip alone**, which is a crude proxy for incompressibility,
  not a formal NIST test.
- **The dead-letter exchange `dlx.quantum` is declared but not wired.** No queue sets
  `x-dead-letter-exchange`, and `Consume` requeues failures with
  `msg.Nack(false, true)`, so nothing is ever routed to the DLX. Declared dead
  lettering; the routing half of the feature is unimplemented backlog.

## Roadmap

The next step is **post-quantum cryptography**: ML-KEM-768 (key encapsulation) and
ML-DSA-65 (signatures) from [CIRCL](https://github.com/cloudflare/circl), fed by the
**same quantum-seeded `xorReader`** — which is precisely why that reader now carries a
mutex. The full plan (phases, parameter sets, HPKE hybrid wrapping, audit signing, open
decisions and risks) lives in [`docs/CIRCL_INTEGRATION_PLAN.md`](docs/CIRCL_INTEGRATION_PLAN.md).

## 👨‍💻 Developed by
**Leporoni Tech Solutions**
📧 [leporonitech@gmail.com](mailto:leporonitech@gmail.com)
