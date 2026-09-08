# Quantum Entropy Service (Go) — Documentação Completa para Desenvolvedores

> **Módulo:** `github.com/leporoni/quantum-entropy-go-service`
> **Linguagem:** Go 1.25
> **Última atualização:** 2026-09-08
>
> Este documento explica o projeto em detalhe: arquitetura, cada biblioteca, cada tipo/função,
> as boas práticas de Go adotadas, os testes do Entropy Audit Lab e tudo o que é necessário
> para novos devs entenderem e evoluírem o código.

---

## Sumário

1. [Visão Geral](#1-visão-geral)
2. [Arquitetura](#2-arquitetura)
3. [Estrutura do Projeto](#3-estrutura-do-projeto)
4. [Stack e Bibliotecas](#4-stack-e-bibliotecas)
5. [Pacotes Internos — Referência de API](#5-pacotes-internos--referência-de-api)
   - 5.1. `internal/quantum`
   - 5.2. `internal/keymanager`
   - 5.3. `internal/messaging`
   - 5.4. `internal/collector`
   - 5.5. `internal/audit`
   - 5.6. `internal/audit/validators` (Entropy Lab — matemática dos testes)
   - 5.7. `internal/audit/suites.go` (registry e vereditos)
   - 5.8. `internal/ui`
   - 5.9. `cmd/` (entrypoints e variáveis de ambiente)
6. [Entropy Audit Lab — Guia](#6-entropy-audit-lab--guia)
7. [Boas Práticas de Go Usadas no Projeto](#7-boas-práticas-de-go-usadas-no-projeto)
8. [Frontend HTMX](#8-frontend-htmx)
9. [Docker e Deploy](#9-docker-e-deploy)
10. [Fluxo End-to-End](#10-fluxo-end-to-end)
11. [Testes](#11-testes)
12. [Convenções de Erros](#12-convenções-de-erros)
13. [Itens Futuros / TODOs](#13-itens-futuros--todos)

---

## 1. Visão Geral

Este é um **microserviço de entropia quântica**. Ele busca números aleatórios verdadeiros de
uma fonte externa (LfD Quantum API), armazena essa entropia em um pool, e a usa para gerar
**chaves RSA criptograficamente superiores** (semeadas com entropia física em vez de apenas
aleatoriedade do SO). É a reescrita em Go de um sistema Java/Spring Boot existente.

Além disso, o projeto contém um **Entropy Audit Lab** que compara estatisticamente a
qualidade da entropia quântica contra geradores de números pseudoaleatórios convencionais
(CSPRNG e LCRNG), usando métricas globais e um subconjunto dos testes do **NIST SP 800-22**.

### Princípios-chave

| Princípio | Detalhe |
|-----------|---------|
| **Event-driven** | Toda mudança de estado relevante emite eventos para o RabbitMQ |
| **Entropia física** | RSA seedado com bytes quânticos reais (via `io.Reader` customizado) |
| **Frontend sem JS** | Interface 100% HTML + HTMX (fragmentos HTML retornados pelo servidor) |
| **Audit replicável** | PRNG de comparação semeado deterministicamente (`seed = 12345`) |
| **Messaging opcional** | Se o RabbitMQ cair, o sistema continua funcionando (degradação graciosa) |

---

## 2. Arquitetura

Dois serviços HTTP (Gin) + broker de mensagens + banco em memória:

```
┌──────────────┐   HTTP    ┌──────────────────┐
│  LfD Quantum │◄──────────│  quantum-api     │  (:8081, stateless)
│  API (QRNG)  │  GET qrng │  coleta + mixing │
└──────────────┘           └────────┬─────────┘
                                    │ GET /api/v1/quantum-random
┌────────────────────────────────────▼─────────┐
│ keymanager (:8082)                           │
│  · pool de entropia (SQLite in-memory)       │
│  · scheduler de coleta (goroutine + ticker)  │
│  · RSA CRUD + AES-256-GCM wrap/unwrap        │
│  · Entropy Audit Lab (suítes + validators)   │
│  · frontend HTMX                             │
└──────────────────────┬───────────────────────┘
                       │ eventos AMQP
                 ┌─────▼───────┐
                 │  RabbitMQ   │  (:5672 / UI :15672)
                 └─────────────┘
```

- **`quantum-api`** — busca bytes da LfD Quantum API e aplica o mixing NIST SP 800-90C
  (opcional, `pure=false`). Não tem banco.
- **`keymanager`** — dono do pool, das chaves, do scheduler, do lab e da UI web.

O pool é um conjunto de registros de 256 bytes de entropia quântica pura.

| Parâmetro | Valor | Onde |
|-----------|-------|------|
| Tamanho de cada registro | 256 bytes | `collector/scheduler.go` |
| Pool cheio (`highWatermark`) | 1000 registros (~256 KB) | `keymanager/service.go:25` e `collector/scheduler.go` |
| Pool baixo (`lowWatermark`) | 200 registros | `keymanager/service.go:24` |
| Consumo por geração de chave | 5 registros (1280 B) | `keymanager/service.go:22` |
| Consumo por exportação | 2 registros | `keymanager/service.go:23` |
| Tick do scheduler | 5 s | `collector/scheduler.go:66` |
| Refill "rápido" entre lotes | 200 ms | `collector/scheduler.go:99` |

> **Importante:** o SQLite é **in-memory** (`file::memory:?cache=shared`). Dados se perdem no
> restart do container — por design, pois entropia/NVRAM não deve persistir.

---

## 3. Estrutura do Projeto

```
cmd/
  quantum-api/main.go          # Entrypoint serviço 1 (coleta/mixing da LfD)
  keymanager/main.go           # Entrypoint serviço 2 (pool, chaves, lab, UI)
internal/
  quantum/                     # Cliente LfD + mixing NIST SP 800-90C
    client_lfd.go              # HTTP client + decoding HEX/JSON
    mixing.go                  # SHA-256 counter-mode keystream
    service.go                 # GetEntropyAsBase64 + MixWithSystemEntropy
    handler.go                 # GET /api/v1/quantum-random
  keymanager/                  # Pool + chaves RSA
    model.go                   # QuantumData e RsaKey (GORM)
    repository.go              # Acesso a dados (transações)
    service.go                 # Negócio: chaves, wrap/unwrap, eventos
    handler.go                 # API REST /api/v1/keys ...
  messaging/                   # RabbitMQ
    connection.go              # Conexão + topologia (exchanges/queues)
    events.go                  # Constantes + structs de eventos
    publisher.go               # Publish com timeout/context
    consumer.go                # Consume + acks (infra pronta, em uso futuro)
  collector/                   # Scheduler de coleta
    scheduler.go               # Goroutine com hysteresis + TriggerRefill
  audit/                       # Entropy Lab
    service.go                 # RunFullAudit + amostragem determinística
    suites.go                  # Registry das 4 suítes + vereditos
    handler.go                 # GET /api/v1/quantum-entropy/audit
    validators/                # Primitivas estatísticas puras
      validators.go            # Shannon, chi-square, pi, gzip, repetições
      bits.go                  # ToBits/ToBitsCountOnes helper
      igamc.go                 # Gamma incompleta + normal CDF
      minentropy.go            # Estimadores SP 800-90B (MCV)
      nist.go                  # Testes NIST SP 800-22
      structure.go             # Bias/autocorrelação/runs/correlação serial
      validators_test.go       # Testes (referências + distribuição)
  ui/                          # Frontend HTMX (fragmentos)
    handler.go                 # Todas as rotas /ui/*
web/
  static/
    index.html                 # SPA cyberpunk (HTMX 1.9)
    css/cyberpunk.css          # Tema neon/glassmorphism
    js/.gitkeep
  templates/doc.go             # Pacote vazio p/ assets (não usado de fato)
```

---

## 4. Stack e Bibliotecas

### 4.1 Bibliotecas externas (go.mod)

| Biblioteca | Versão | O que faz neste projeto |
|------------|--------|--------------------------|
| `github.com/gin-gonic/gin` | v1.12.0 | Framework HTTP (rotas, grupos, JSON, servidor de estático). O `gin.Default()` cria um engine com logger + recovery. `SetTrustedProxies(nil)` desliga parsing de proxy. |
| `gorm.io/gorm` | v1.30.0 | ORM. Mapeia `QuantumData`/`RsaKey` → tabelas, `AutoMigrate`, transações, query builder. |
| `gorm.io/driver/sqlite` | v1.6.0 | Driver SQLite (via `mattn/go-sqlite3`, que exige **CGO ativo**). |
| `github.com/rabbitmq/amqp091-go` | v1.10.0 | Cliente AMQP 0-9-1 oficial. `PublishWithContext`, `ExchangeDeclare`, `Consume`, `Ack/Nack`. |

### 4.2 Pacotes da stdlib usados

| Pacote | Para quê | Onde (exemplos) |
|--------|----------|------------------|
| `crypto/rsa` | `rsa.GenerateKey(reader, bits)` — gera par RSA usando um `io.Reader` customizado q uântico | `keymanager/service.go` |
| `crypto/x509` | `MarshalPKIXPublicKey` (pública) e `MarshalPKCS1PrivateKey` (privada → PEM) | `keymanager/service.go` |
| `crypto/aes` + `crypto/cipher` | AES-256-GCM (`cipher.NewGCM`) para wrap/unwrap da chave privada | `keymanager/service.go` (helpers `aesGCMEncrypt/Decrypt`) |
| `crypto/sha256` | Deriva a chave mestra (`SHA-256(MASTER_KEY_SECRET)`) e o keystream do mixing | `keymanager/service.go`, `quantum/mixing.go` |
| `crypto/rand` | CSPRNG do SO; usado no mixing, no `xorReader` e no `getCsprngSample` | vários |
| `encoding/pem`, `encoding/base64`, `encoding/hex`, `encoding/json` | PEM de chaves, base64 no pool/API, HEX na LfD, JSON nos handlers/eventos | vários |
| `compress/gzip` | Mede "Compression Ratio" na auditoria (`gzip.BestCompression`) | `validators/validators.go` |
| `math` + `math/rand` | Estatística dos validators (erfc, lgamma, sqrt) e PRNG determinístico do lab | `validators/*`, `audit/service.go` |
| `log/slog` | Logging estruturado (única forma de log do projeto) | todos os pacotes |
| `net/http`, `os/signal`, `context`, `time` | Servidor HTTP com graceful shutdown, HTTP clients com timeout | `cmd/*` |
| `fmt`, `errors`, `strconv`, `strings`, `io`, `bytes`, `sync`*, `testing` | Diversos (ver 4.3) | vários |

> `* sync` e `math/big` aparecem em leitura mas **não são usados** atualmente.

### 4.3 Padrões de biblioteca no código (cheat-sheet)

```
gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})  # cmd/keymanager/main.go
r.db.Create(q).Error                                                    # repository.go
r.db.Transaction(func(tx *gorm.DB) error { ... })                       # ConsumeEntropy
r.db.Unscoped().Delete(&RsaKey{}, id)                                   # hard delete
errors.Is(err, gorm.ErrRecordNotFound)                                  # FindKeyByID

amqp.Dial(url) → c.channel.ExchangeDeclare(name, "topic", durable, ...) # connection.go  (topic exchanges)
c.channel.QueueDeclare / QueueBind(queue, routingKey, exchange)          # connection.go
p.conn.Channel().PublishWithContext(ctx, exch, rk, false, false, amqp.Publishing{...}) # publisher.go

c.JSON(http.StatusOK, gin.H{...})     # respostas REST
c.Data(http.StatusOK, "text/html", ...) # fragmentos HTMX
c.Query("size") / c.Param("id") / c.PostForm("alias") / c.ShouldBindJSON(&req)
```

---

## 5. Pacotes Internos — Referência de API

### 5.1 `internal/quantum` — coleta e mixing

**`client_lfd.go`**
```go
type LfdApiResponse struct { Qrn string `json:"qrn"` }
type LfdClient struct { baseURL string; httpClient *http.Client }

func NewLfdClient(baseURL string) *LfdClient
func (c *LfdClient) FetchRandomBytes(count int) ([]byte, error)
```
- `NewLfdClient("")` usa o default `https://lfdr.de/qrng_api`.
- `FetchRandomBytes` faz `GET {base}/qrng?length={count}&format=HEX`, lê `{"qrn": "<hex>"}`
  e decodifica para bytes. Timeout HTTP de 30 s.

**`mixing.go`** — NIST SP 800-90C (mixing) com primitivas locais:
- `generateSystemEntropy(size)` → `crypto/rand.Read`.
- `generateKeystream(key, length)` → keystream em modo contador SHA-256:
  `SHA-256(key ‖ counter_BE32)` repetido. (Não é AES-CTR; é construção SHA-256-CTR.)

**`service.go`**
```go
const MaxCount = 1024
func NewService(lfdClient *LfdClient) *Service
func (s *Service) GetEntropyAsBase64(count int, pure bool) (string, error)
func MixWithSystemEntropy(quantumBytes []byte) ([]byte, error)
```
`MixWithSystemEntropy` (o coração do mixing):
1. 32 bytes de entropia do SO;
2. `mixingKey = SHA-256(quantumBytes ‖ systemEntropy)`;
3. gera keystream do mesmo tamanho de `quantumBytes`;
4. `mixed[i] = quantumBytes[i] XOR keystream[i]`.

**`handler.go`** — `GET /api/v1/quantum-random?source=LFD&count=128&pure=false` →
`{"data": "<base64>"}`. Defaults: `count=128`, `pure=false`.

### 5.2 `internal/keymanager` — pool + chaves

**`model.go`**
```go
type QuantumData struct {
    gorm.Model                 // ID, CreatedAt, UpdatedAt, DeletedAt (embedding)
    DataBase64 string `gorm:"not null"`
    Used       bool   `gorm:"default:false;index"`
    Source     string `gorm:"not null;index"`
}
type RsaKey struct {
    gorm.Model
    Alias               string `gorm:"uniqueIndex;not null"`
    KeySize             int    `gorm:"not null"`
    PublicKeyPEM        string `gorm:"not null"`
    EncryptedPrivatePEM []byte `gorm:"not null"`
    Nonce               []byte `gorm:"not null"`   // nonce do AES-GCM
}
```

**`repository.go`** (camada de dados; retorna erros crus do GORM)
```go
func NewRepository(db *gorm.DB) (*Repository, error)   // roda AutoMigrate
// QuantumData:
func (r *Repository) SaveEntropy(q *QuantumData) error
func (r *Repository) CountAllUnusedEntropy() (int64, error)
func (r *Repository) FindAllUnusedBySource(source string) ([]QuantumData, error)
func (r *Repository) ConsumeEntropy(n int) ([]QuantumData, error)  // transacional
// RsaKey:
func (r *Repository) SaveKey(k *RsaKey) error
func (r *Repository) FindAllKeys() ([]RsaKey, error)
func (r *Repository) FindKeyByID(id uint) (*RsaKey, error)         // (nil,nil) se não achar
func (r *Repository) DeleteKeyByID(id uint) error                  // hard delete (Unscoped)
func (r *Repository) DeleteAllKeys() error
```
`ConsumeEntropy(n)`: dentro de uma transação, busca `n` registros `used=false`, valida que há
pelo menos `n` (senão erro `"insufficient entropy in pool"`), marca `used=true` e retorna.
É **atômico** (evita corrida entre duas gerações simultâneas).

**`service.go`** (regras de negócio)
```go
const (
    entropyPerKey     = 5
    entropyPerExport  = 2
    poolLowThreshold  = 200
    poolHighThreshold = 1000
)
type Service struct {
    repo      *Repository
    masterKey []byte                 // AES-256 key (SHA-256 do MASTER_KEY_SECRET)
    pub       *messaging.Publisher   // pode ser nil
    OnPoolLow func()                 // callback setado no main: scheduler.TriggerRefill
}
func NewService(repo, masterKeySecret string, pub) (*Service, error)
func (s *Service) GenerateKey(alias string, keySize int) (*RsaKey, error)
func (s *Service) ExportPrivateKey(id uint) ([]byte, error)
func (s *Service) PoolStatus() (int64, error)
func (s *Service) DeleteKey(id uint) error
func (s *Service) DeleteAllKeys() error
```
**`GenerateKey` (fluxo):**
1. `ConsumeEntropy(5)` → 5 registros (1280 B) de entropia quântica.
2. `buildQuantumSeed(records)` → concatenação dos bytes decodificados.
3. `newXORReader(seed)` → `io.Reader` que lê do `crypto/rand` e faz **XOR** com o seed q uântico
   (`xorReader.Read`). Cada byte aleatório que o RSA consome é misturado com entropia física.
4. `rsa.GenerateKey(seededReader, keySize)` — o Go lê TODA aleatoriedade primal necessária
   desse reader.
5. Publica/privada → PEM (`MarshalPKIXPublicKey` / `MarshalPKCS1PrivateKey`).
6. `aesGCMEncrypt(privPEM)` → `(ciphertext, nonce)` usando a chave mestra.
7. `SaveKey` + publica `key.created` + `checkAndPublishPoolEvent()`.
8. Se sobrou `< 200` → chama `s.OnPoolLow()` (dispara refill) e publica `entropy.pool.low`.

**Helpers (privados):**
```go
func (s *Service) aesGCMEncrypt(plaintext []byte) (ciphertext, nonce []byte, err error)  // named returns
func (s *Service) aesGCMDecrypt(ciphertext, nonce []byte) ([]byte, error)
func buildQuantumSeed(records []QuantumData) []byte
type xorReader struct{ seed []byte; offset int }   // satisfaz io.Reader (interface)
func newXORReader(seed []byte) io.Reader
```

**`handler.go`** — REST sob `/api/v1`:
- `POST   /keys` → gerar (`{alias, keySize?}`)
- `GET    /keys` → listar
- `DELETE /keys` e `/keys/:id` → deletar (todas / uma)
- `POST   /keys/:id/export` → PEM da privada (unwrap AES-GCM)
- `GET    /quantum-entropy/status` → `{"availableRecords": N}`

### 5.3 `internal/messaging` — RabbitMQ

**`connection.go`**
```go
func NewConnection(url string) (*Connection, error)   // 5 tentativas com backoff (2s..10s)
func (c *Connection) Channel() *amqp.Channel
func (c *Connection) Close()
func (c *Connection) DeclareExchange(name string) error                      // topic, durable
func (c *Connection) DeclareQueue(queueName, exchange, routingKey string) (amqp.Queue, error)
func (c *Connection) DeclareDeadLetterExchange(dlxName string) error         // fanout + DLX
```

**`events.go` — topologia (constantes + structs)**

Exchanges (todas `topic`):
```
entropy.collected   → entropy.new / entropy.validated
key.events          → key.created / key.exported / key.deleted
audit.requests      → audit.start
audit.results       → audit.complete
entropy.pool        → entropy.pool.low / entropy.pool.ok
dlx.quantum         → (fanout, dead-letter)
```

Filas (durable, ligadas por binding):
| Fila | Exchange | Routing Key |
|------|----------|-------------|
| `q.entropy.new` | `entropy.collected` | `entropy.new` |
| `q.entropy.validated` | `entropy.collected` | `entropy.validated` |
| `q.key.created` | `key.events` | `key.created` |
| `q.key.exported` | `key.events` | `key.exported` |
| `q.key.deleted` | `key.events` | `key.deleted` |
| `q.audit.start` | `audit.requests` | `audit.start` |
| `q.audit.complete` | `audit.results` | `audit.complete` |
| `q.pool.low` | `entropy.pool` | `entropy.pool.low` |
| `q.pool.ok` | `entropy.pool` | `entropy.pool.ok` |

Eventos (JSON):
- `EntropyNewEvent{source, base64Data, byteCount, timestamp}`
- `EntropyValidatedEvent{id, source, byteCount, poolSize, timestamp}`
- `KeyCreatedEvent{id, alias, keySize, timestamp}`
- `KeyExportedEvent{id, alias, algorithm, timestamp}`
- `KeyDeletedEvent{id, alias, timestamp}`
- `AuditStartEvent{requestedSize, timestamp}`
- `AuditCompleteEvent{sampleSize, results, timestamp}`
- `PoolLowEvent{currentCount, threshold, timestamp}`
- `PoolOkEvent{currentCount, threshold, timestamp}`

**`publisher.go`**
```go
func NewPublisher(conn *Connection) *Publisher
func (p *Publisher) Publish(exchange, routingKey string, event interface{}) error
```
JSON-marshal do evento + publica com `ContentType=application/json`,
`DeliveryMode=amqp.Persistent`, timeout de 5 s via `context.WithTimeout`.

**`consumer.go`** — infraestrutura pronta, atualmente **sem consumidores em produção**:
```go
type MessageHandler func(body []byte) error
func NewConsumer(conn *Connection) *Consumer
func (c *Consumer) Consume(queueName string, handler MessageHandler) error
func (c *Consumer) ConsumeWithPrefetch(queueName string, prefetch int, handler MessageHandler) error
func SetupExchangesAndQueues(conn *Connection) error   // declara toda a topologia
```
Ack manual: se o handler falhar → `msg.Nack(false, true)` (requeue); se ok → `msg.Ack(false)`.

### 5.4 `internal/collector` — scheduler de coleta

```go
type Scheduler struct {
    repo          *keymanager.Repository
    pub           *messaging.Publisher
    apiBaseURL    string
    httpClient    *http.Client          // timeout 30s
    lowWatermark  int64                 // 200
    highWatermark int64                 // 1000
    stopChan      chan struct{}
    refillChan    chan struct{}         // capacidade 1 (coalesce sinais)
}
func NewScheduler(repo, apiBaseURL, pub) *Scheduler
func (s *Scheduler) TriggerRefill()     // envia sinal não-bloqueante para refillChan
func (s *Scheduler) Start()             // go s.run()
func (s *Scheduler) Stop()              // fecha stopChan
```
- `run()`: `time.NewTicker(5 * time.Second)`; em cada ciclo faz `select` entre
  `stopChan`, `refillChan` e `ticker.C`.
- `collectEntropy()`: se `count < 200`, entra em loop de refill rápido: busca 1 lote
  (`fetchAndSave()` = 256 bytes `pure=true`), dorme 200 ms entre sucessos, 2 s em erro.
  Para ao atingir `count >= 1000` ou após `maxFailures = 10` erros consecutivos.
- `fetchAndSave()`: `GET {api}/api/v1/quantum-random?count=256&pure=true`, valida base64,
  salva `QuantumData{Source:"LFD"}`, publica `entropy.new`. Retorna `bool` (sucesso/falha).

### 5.5 `internal/audit` — auditoria

**`service.go`**
```go
type AuditMetrics struct {
    Source           string  `json:"source"`
    ShannonEntropy   float64 `json:"shannonEntropy"`
    ChiSquare        float64 `json:"chiSquare"`
    PiEstimate       float64 `json:"piEstimate"`
    CompressionRatio float64 `json:"compressionRatio"`
    Repetitions      int     `json:"repetitions"`
    Base64Sample     string  `json:"base64Sample"`
    FingerprintHex   string  `json:"fingerprintHex"`   // primeiros 16 bytes em hex (traceability)
}
type AuditReport struct {
    SampleSize int            `json:"sampleSize"`
    Results    []AuditMetrics `json:"results"`
}
func NewService(repo *keymanager.Repository, pub *messaging.Publisher) *Service
func (s *Service) RunFullAudit(requestedSize int) (*AuditReport, error)
```
`RunFullAudit`: publica `audit.start`, amostra **Quantum (LFD)** do pool, gera amostras de
**CSPRNG** (`crypto/rand`) e **PRNG** (`math/rand` seed `12345`) do mesmo tamanho, e roda as
5 métricas para cada fonte.

**`handler.go`** — `GET /api/v1/quantum-entropy/audit?size=8192` (JSON `AuditReport`).

**Amostragem determinística (`service.go`):**
```go
func getCsprngSample(size int) []byte                    // crypto/rand
func getPrngSample(size int, seed int64) []byte          // math/rand seeded
func (s *Service) getQuantumSample(source string, size int) ([]byte, error)
```
O PRNG de comparação é seeded (`seed=12345` por padrão) para que todo auditorial seja
**replicável** entre execuções.

### 5.6 `internal/audit/validators` — matemática dos testes

> Todos os validators são **funções puras** (sem I/O, sem estado) — só entrada → saída.
> Isso facilita testar e reutilizar.

**`validators.go` (métricas globais)**

| Função | Fórmula / comportamento |
|--------|--------------------------|
| `CalculateShannonEntropy(data []byte) float64` | `H = −Σ p·log2(p)` sobre 256 símbolos; uniforme ≈ 8.0 |
| `CalculateChiSquare(data []byte) float64` | Pearson `χ² = Σ (freq − n/256)² / (n/256)`; aleatório ≈ 255 |
| `EstimatePiMonteCarlo(data []byte) float64` | pares `(x,y)∈[0,1)²`, conta `x²+y²≤1` → `4·inside/total`; ≈ π |
| `CalculateCompressionRatio(data []byte) float64` | `len(gzip(data))/len(data)` (BestCompression); aleatório ≈ 1.0 |
| `CountRepetitions(data []byte) int` | nº de pares consecutivos idênticos; esperado `(n−1)/256` |

**`bits.go`**
```go
func ToBits(data []byte) []uint8   // expande bytes → 8 bits MSB-first (0/1)
func countOnes(bits []uint8) int   // (privada) contagem de bits setados — usada por NIST/Monobit
```

**`igamc.go` — gamma incompleta (base para p-valores NIST)**
```go
func igamc(a, x float64) float64   // Q(a,x) = Γ(a,x)/Γ(a) — regulariz. upper incomplete gamma
func normalCDF(x float64) float64  // Φ(x) = 0.5·erfc(−x/√2)
```
`igamc` espelha *Numerical Recipes* `gammq`:
- `x == 0` → `1`.
- ramo da **série** (`x < a+1`): `P(a,x) = x^a·e^{−x}·Σ …`, retorna `1 − P`.
- ramo da **fração contínua** (Lentz, `x ≥ a+1`): itera até convergir
  (`ε = 3e-7`, piso `1e-300`), retorna `exp(−x + a·ln x − lnΓ(a))·h`.
- Usa `math.Lgamma(a)` (cuidado: retorna **2 valores**, `(lgamma, sign)`).

**`minentropy.go` — estimadores SP 800-90B**
```go
func EstimateMinEntropyMCV(data []byte) float64   // H_min = −log2(p̂), p̂ = MCV/n  (bits/byte)
func EstimateMinEntropyBits(data []byte) float64  // análogo ao nível de bit (bits/bit)
func MostCommonValue(data []byte) int             // contagem do byte mais frequente
func DistinctByteValues(data []byte) int          // n° de valores distintos observados
func ExpectedDistinctValues(n int) float64        // 256·(1 − (255/256)^n)
```
- **MCV (Most Common Value)** = estimador de *counting* da SP 800-90B §6.3.1. Para dados
  uniformes, o MCV ≈ `n/256` (≈ 4096 em 1 MiB) → `H_min ≈ 8 bits/byte`.

**`nist.go` — subconjunto NIST SP 800-22** (todas recebem `bits []uint8`)

| Função | Teste | Estatística |
|--------|-------|-------------|
| `NISTMonobit(bits)` | Frequency (Monobit) | `s = |2·ones − n|/√n`; `p = erfc(s/√2)` |
| `NISTBlockFrequency(bits, m)` | Frequency within a Block | `χ² = 4m·Σ(πᵢ−0.5)²`; `p = Q(blocks/2, χ²/2)` — usado com `M=128` |
| `NISTRuns(bits)` | Runs | pré-condição `|π−0.5| ≥ 2/√n → p=0`; senão `erfc(|V − 2nπ(1−π)| / (2√n π(1−π)√2))` |
| `NISTLongestRunOfOnes(bits)` | Longest Run of Ones | tiers fixos M=8/N=16 (n<6272) ou M=128/N=49; `p = Q(K/2, χ²/2)` |
| `NISTApproximateEntropy(bits, m)` | Approximate Entropy (m=5) | `χ² = 2n(ln2 − (φₘ − φₘ₊₁))`; `p = Q(2^{m−1}, χ²/2)` |
| `NISTSerial(bits) (p1,p2,m)` | Serial | `m` adaptativo ∈ [3,16] com `2m·2^m ≤ n`; `p1=Q(2^{m−2},d1/2)`, `p2=Q(2^{m−3},d2/2)` |
| `NISTCumulativeSums(bits) (pForward,pReverse)` | Cumulative Sums | fiel ao `cusum.c` de referência |
| `cumulativeSumsP(n, z)` (privado) | — | `p = 1 − sum1 + sum2`; laços `k` com **divisão inteira truncada** `(±n/z±1)/4` |

Detalhes importantes de `NISTLongestRunOfOnes` (armadilha clássica):
- NIST usa **número fixo** de blocos `N` sobre os **primeiros `N·M` bits** (não todos os blocos).
- Mapeamento de bins: `bin0 = run ≤ V[0]`, `binj = run == V[j]` (1..K−1), `binK = run ≥ V[K]`.
- Tabelas de referência:
  - M=8: `V=[1,2,3,4]`, `π=[0.2148,0.3672,0.2305,0.1875]`
  - M=128: `V=[4,5,6,7,8,9]`, `π=[0.1174,0.2430,0.2493,0.1752,0.1027,0.1124]`

Detalhes de `NISTCumulativeSums` (bug corrigido recentemente, veja §11/§12):
- Caminhada `+1`/`−1`; `z = max(sup, −inf)`, `zrev = max(sup−sum, sum−inf)`.
- Fórmula do NIST **`p = 1 − sum1 + sum2`** (o termo `sum2` é **soma**, não subtração —
  foi onde estava o bug).
- Limites `k` com divisão inteira truncada (semântica de C): `for k := (‑nz+1)/4; k <= (nz‑1)/4`.
- Implementado espelhando o `cusum.c` do STS 2.1a.

**`structure.go` — análise de ordem/correlação**
```go
func StructureBitBias(data []byte) float64                       // max |z| entre as 8 posições de bit
func StructureAutocorrelation(data []byte, maxLag int) (maxZ float64, outOfRange int, worstLag int)
func StructureRunsZ(bits []uint8) float64
func StructureSerialCorrelation(data []byte) (r, z float64)
```

| Função | O que faz |
|--------|-----------|
| `StructureBitBias` | Para cada bit position `i%8`, z-score de `p=ones/total` vs 0.5 → retorna o maior `|z|` |
| `StructureAutocorrelation` | Para `lag=1..maxLag`, `p=agree/pairs`, `z=|2p−1|·√pairs` → max, contagem de outliers (`z>2`), lag pior |
| `StructureRunsZ` | Wald–Wolfowitz com variância exata; **`+Inf`** se sequência monótona; `MaxFloat64` se variância ≤ 0 |
| `StructureSerialCorrelation` | Pearson entre bytes consecutivos (lag 1) + `z=|r|·√n` |

### 5.7 `internal/audit/suites.go` — registry de suítes e vereditos

**Tipos:**
```go
const DefaultPRNGSeed int64 = 12345
var ErrUnknownSuite = errors.New("unknown suite")

type Verdict string
const (
    VerdictPass Verdict = "pass"
    VerdictWarn Verdict = "warn"
    VerdictFail Verdict = "fail"
)

type Metric struct { Name, Value, Reference string; Verdict Verdict }
type SourceResult struct { Source string; Metrics []Metric }

type SuiteResult struct {
    SuiteID, Name, Description, MinNote string
    SampleSize int
    Indicative bool
    Results []SourceResult
}

type suiteDef struct {           // (privado — estratégia de execução por suíte)
    id, name, description, minNote string
    minBytes int
    run func(data []byte) []Metric
}
```

**Registry (as 4 suítes do lab):**

| `id` | Nome | minBytes | Descrição |
|------|------|----------|-----------|
| `basic` | Basic | 1024 | Métricas globais com vereditos automáticos. "1–16 KB is enough." |
| `min-entropy` | Min-Entropy | 1 MiB (`1<<20`) | Estimadores MCV (SP 800-90B). "Best with ≥ 1M samples; below: INDICATIVE." |
| `nist` | NIST SP 800-22 | 128 KiB (`1<<17`) | Subset prático (p-valores, α=0.01). |
| `structure` | Structure | 64 KiB (`1<<16`) | Bias, autocorrelação, runs, correlação serial. |

**Veredito — helpers:**
```go
bandVerdict(v, okLow, okHigh, warnLow, warnHigh float64) Verdict // faixa central = pass
highVerdict(v, okThreshold, warnThreshold float64) Verdict       // "quanto maior melhor"
lowVerdict(v, okThreshold, warnThreshold float64) Verdict        // "quanto menor melhor"
zVerdict(z float64) Verdict                                      // |z|<2 pass, <3 warn, senão fail
pVerdict(p float64) Verdict                                      // p≥0.01 pass; 0.001≤p<0.01 warn; senão fail
fmtP(p float64) string                                           // NaN → "n/a"; p<1e-4 → %.3e
```
Limiares usados por suíte:
- **basic**: Shannon `≥7.9` (warn `≥7.5`); χ² em `[200,310]` (warn `[170,350]`); Pi em
  `[3.12,3.16]` (warn `[3.06,3.22]`); Compression `≤1.08` (warn `≤1.20`); Repetitions ratio `≤2` (warn `≤4`).
- **min-entropy**: 8-bit `≥7.9` (warn `≥7.0`); bit-level `≥0.99` (warn `≥0.95`);
  MCV prob `≤0.0042` (warn `≤0.0080`); distinct ratio `≥0.99` (warn `≥0.95`).
- **nist**: todas `pVerdict` com α=0.01.
- **structure**: `zVerdict` nas métricas de z; `lowVerdict(out, 0, 1)` para nº de lags outliers.

**`RunSuites` (`suites.go:100`):**
```go
func (s *Service) RunSuites(suiteID string, requestedSize int, seed int64) (*SuiteResult, error)
```
1. Busca a `suiteDef` no registry; inexistente → `ErrUnknownSuite`.
2. `seed == 0` → `DefaultPRNGSeed`.
3. Amostra **Quantum (LFD)** do pool (`getQuantumSample("LFD", size)`). Se não houver dados →
   resultado vazio (UI mostra mensagem).
4. Se a amostra quântica existe, roda a suíte nas **3 fontes** com o mesmo tamanho real:
   - `Quantum (LFD)` — bytes reais do pool.
   - `Java SecureRandom (CSPRNG)` — `crypto/rand`.
   - `Java Random (LCRNG)` — `math/rand` semeado.
5. `Indicative = realSampleSize < def.minBytes` → a UI exibe banner "indicative".

> **Por que "Java ..." nos rótulos da fonte?** O projeto original é um port de um sistema
> Java, cuja auditoria comparava o gerador quântico com `SecureRandom` e `Random` do Java.
> Os rótulos foram preservados para manter a paridade conceitual.

### 5.8 `internal/ui` — frontend HTMX

```go
type Handler struct { svc *keymanager.Service; repo *keymanager.Repository; auditSvc *audit.Service }
func NewHandler(svc, repo, auditSvc) *Handler
func (h *Handler) RegisterRoutes(r *gin.Engine)
```
Rotas (todas retornam **fragmentos HTML**, não páginas completas):

| Rota | Handler | Conteúdo |
|------|---------|----------|
| `GET /` | inline | serve `index.html` |
| `GET /static/*` | Gin Static | arquivos estáticos |
| `GET /ui/pool-status` | `poolStatus` | barra do pool, contagem/1000, KB disponíveis (polling 3 s) |
| `GET /ui/system-status` | `systemStatus` | badges online/offline (checa quant-api e RabbitMQ **server-side**) |
| `GET /ui/rabbitmq-queues` | `rabbitmqQueues` | proxy do Management API (`/api/queues`) → tabela (polling 5 s) |
| `GET /ui/keys` | `listKeys` | tabela de chaves com botões Export/Delete |
| `POST /ui/keys` | `generateKey` | form (alias, keySize) → lista atualizada |
| `DELETE /ui/keys` | `deleteAllKeys` | limpa tudo → empty state |
| `DELETE /ui/keys/:id` | `deleteKey` | remove a `<tbody id="key-row-N">` (swap outerHTML) |
| `POST /ui/keys/:id/export` | `exportKey` | PEM inline com botão copy (dentro do `<tr>` modal) |
| `GET /ui/audit` | `runAudit` | grid de audit-cards (auditoria `RunFullAudit`) |
| `GET /ui/lab` | `runLab` | lab: `?suite=&size=&seed=` → lab-meta + banner (se indicativa) + cards (basic) ou `lab-table` |

Detalhe do `runLab`: para `basic` renderiza `.audit-grid` com cards; para as demais suítes
renderiza a tabela `.lab-table` (Source / Metric / Value / Reference / Verdict) com badges
coloridos `.verdict-pass|warn|fail`.

### 5.9 `cmd/` — entrypoints e env vars

**`cmd/quantum-api/main.go`** — `PORT` (default `8081`), `LFD_API_URL` (default LfD). Graceful shutdown com `signal.Notify(SIGINT, SIGTERM)` + `srv.Shutdown(ctx, 10s)` + `select`.

**`cmd/keymanager/main.go`** — ordem de inicialização:
1. SQLite in-memory via GORM.
2. `NewRepository` (AutoMigrate).
3. RabbitMQ (`NewConnection` + `SetupExchangesAndQueues`) — se indisponível, **warn e segue**.
4. `NewPublisher` → `NewScheduler` → `NewService`.
5. `svc.OnPoolLow = scheduler.TriggerRefill` (callback).
6. `scheduler.Start()` (`defer scheduler.Stop()`).
7. Handlers (keymanager, audit, ui) + rotas em `:8082`.

| Variável | Serviço | Default | Obrigatória |
|----------|---------|---------|-------------|
| `PORT` | ambos | 8081/8082 | não |
| `MASTER_KEY_SECRET` | keymanager | — | **sim** |
| `API_BASE_URL` | keymanager | `http://quantum-api:8081` | não |
| `RABBITMQ_URL` | keymanager | `amqp://guest:guest@rabbitmq:5672/` | não |
| `RABBITMQ_MGMT_HOST` | keymanager | `rabbitmq:15672` | não |

---

## 6. Entropy Audit Lab — Guia

### Como executar

Local (sem Docker):
```bash
go test ./...          # todos os testes (validators)
go run ./cmd/keymanager   # server 8082 (precisa de MASTER_KEY_SECRET)
```
Via UI: aba **Entropy Lab** → 4 sub-abas (`basic`, `min-entropy`, `nist`, `structure`),
cada uma com seletor de tamanho (1–256 KB) e botão **Run**.

Via API:
```bash
curl "http://localhost:8082/api/v1/quantum-entropy/audit?size=8192" | jq   # auditoria antiga (JSON)

# Suítes do lab:
curl "http://localhost:8082/ui/lab?suite=nist&size=131072&seed=12345"
curl "http://localhost:8082/ui/lab?suite=min-entropy&size=262144"
curl "http://localhost:8082/ui/lab?suite=structure&size=65536"
curl "http://localhost:8082/ui/lab?suite=basic&size=8192"
```

### O que cada suíte responde

| Suíte | Pergunta que responde |
|-------|------------------------|
| **basic** | "A fonte é uniforme e não compressível?" (métricas globais) |
| **min-entropy** | "Quanta entropia de fato existe por byte/bit?" (pior caso MCV) |
| **nist** | "Existe padrão estatístico detectável por testes do NIST SP 800-22?" |
| **structure** | "Existe enviesamento de bit ou correlação em sequências curtas?" |

### Limitação importante (indicative)

O pool quântico = no máx. ~256 KB (1000 × 256 B). Testes NIST/min-entropy exigem muito mais
para serem formais (ex.: Serial m=16 precisa `2·m·2ᵐ ≈ 2M bits`; MCV ideal ≥ 1M amostras).
Por isso:

- As suítes rodam com **`m` adaptativo** (maior `m` tal que `2m·2^m ≤ n`);
- resultados abaixo do `minBytes` são rotulados **indicative** na UI;
- nesses tamanhos, **CSPRNG/PRNG** ainda podem ser avaliados com amostras grandes (comparação
  mostra o quantum no seu melhor tamanho disponível).

---

## 7. Boas Práticas de Go Usadas no Projeto

### 7.1 Operador `:=` (declaração curta)

`:=` **declara e inicializa** uma variável inferindo o tipo — sem declarar o tipo
explicitamente. Só pode ser usado **quando ao menos uma variável do lado esquerdo é nova** no
escopo corrente; se a variável já existe, usa-se `=`.

Exemplos no projeto:

```go
// 1) Declaração + inferência de tipo (a forma mais comum):
db, err := gorm.Open(...)          // cmd/keymanager/main.go — tipo inferido de gorm.Open
r := mrand.New(mrand.NewSource(seed)) // audit/service.go:149
resp, err := c.httpClient.Get(url)    // quantum/client_lfd.go:44

// 2) Com erro em multi-retorno — o erro é espalhado na mesma linha:
key, err := h.svc.GenerateKey(req.Alias, req.KeySize)   // keymanager/handler.go

// 3) Criação de ponteiro com &: return &Service{...}    → usado em todos os construtores

// 4) Descarte de valor não usado com _:
seed, _ := strconv.ParseInt(c.Query("seed"), 10, 64)  // ui/handler.go:243
r.Read(sample) // _ = err  (math/rand.Read nunca falha) — audit/service.go
```

Regra prática adotada: `:=` dentro de funções para tudo que é novo; `=` apenas para
**reaproveitar** variável já declarada (ex.: `c.conn, err = amqp.Dial(...)` dentro de um loop
de retry onde `var err error` já foi declarado — `messaging/connection.go:28-31`).

### 7.2 Outras boas práticas observadas

| Prática | Exemplos no projeto |
|---------|----------------------|
| **`(result, err)` como convenção** | Todas as funções "falíveis" retornam `(T, error)`. Nunca há exceções. |
| **Erros embrulhados com `%w`** | `fmt.Errorf("pool exhausted: %w", err)` — preserva a cadeia para `errors.Is`. |
| **Checagem imediata de erro** | `if err != nil { return ...; }` logo após a chamada (early return). |
| **Naming/exports** | Exportado = maiúsculo (`NewService`, `RunSuites`); privado = minúsculo (`registry`, `cumulativeSumsP`). |
| **Construtores** | Padrão `NewX` retornando `(*X, error)` (construtores falíveis) ou `*X` (não falíveis). |
| **Struct embedding** | `QuantumData`/`RsaKey` embutem `gorm.Model` (reutiliza ID/timestamps/soft-delete). |
| **Recievers pointer** | Serviços/repositórios usam sempre ponteiro (`func (s *Service)`...) para não copiar estado. |
| **Interfaces implícitas** | `xorReader` satisfaz `io.Reader` só por implementar `Read`; nenhuma declaração explícita. |
| **`defer` para cleanup** | `defer resp.Body.Close()`, `defer ticker.Stop()`, `defer cancel()`, `defer scheduler.Stop()`. |
| **Goroutines + canais** | Scheduler usa `select`/channels `stopChan`/`refillChan` (buffer 1 p/ coalescer sinais). |
| **`select` com `default`** | `case s.refillChan <- struct{}{}: default:` → sinal não-bloqueante (não enche buffer). |
| **Constantes tipadas** | Blocos `const ( entropyPerKey = 5; ... )` — magic numbers centralizados. |
| **`make` com capacidade** | `make(chan struct{}, 1)`, `make([]uint8, 0, len(data)*8)` — pré-aloca para performance. |
| **Struct literals nomeados** | `&QuantumData{DataBase64: x, Used: false, Source: "LFD"}` — nunca posicional. |
| **Sentinela de erro** | `ErrUnknownSuite` + `errors.Is`. |
| **Logging estruturado** | Só `slog` com pares chave-valor (`slog.Info("msg", "id", id)`), sem `fmt.Println`. |
| **Variádicos/função-tipo** | `func (p *Publisher) Publish(exchange, routingKey string, event interface{})` e `type MessageHandler func(body []byte) error`. |
| **Named returns** | `func (s *Service) aesGCMEncrypt(...) (ciphertext, nonce []byte, err error)` — documenta o retorno. |
| **`min` próprio + builtin** | `audit/service.go` define `func min(a,b int) int` local (que **sobrescreve** o builtin do Go 1.21 dentro do pacote); em `validators_test.go` usa `for i := range out`. |
| **`GOTOOLCHAIN` / toolchain** | go.mod declara `go 1.25.0`; build local com toolchain baixada. |

### 7.3 Detalhes "Go-específicos" nos validators

- `math.Lgamma(a)` retorna **`(lgamma, sign)`** → código usa `gln, _ := math.Lgamma(a)`.
- Shift de constante untyped precisa de tipo explícito: `one := int64(1)` antes de
  `float64(one << uint(m-1))` (em `nist.go`).
- Conversão `[]uint8 → int → float64` é explícita e recorrente nos validators.
- Div. inteira de `int` trunca em direção a zero em Go — exatamente o que o `cusum.c` do NIST
  espera (por isso `cumulativeSumsP` usa `(±n/z±1)/4` sem `floor`).

---

## 8. Frontend HTMX

### 8.1 Como funciona

Não há framework JS. O servidor devolve **fragmentos HTML** prontos; o HTMX injeta no DOM.
O `hx-trigger` controla quando disparar: no `load`, a cada N segundos (polling) ou no click/submit.

Padrões usados em `index.html`:

| Elemento | Padrão HTMX |
|----------|-------------|
| Pool status | `hx-get="/ui/pool-status" hx-trigger="load, every 3s"` |
| System status | `hx-get="/ui/system-status" hx-trigger="load, every 10s"` |
| RabbitMQ queues | `hx-get="/ui/rabbitmq-queues" hx-trigger="load, every 5s"` |
| Gerar chave | `hx-post="/ui/keys" hx-target="#keys-list"` + `hx-on::after-request="clearForm(this); showToast(event)"` |
| Deletar chave | `hx-delete="/ui/keys/{id}" hx-target="#key-row-{id}" hx-swap="outerHTML" hx-confirm="..."` |
| Botões Run do lab | `hx-get="/ui/lab?suite={s}" hx-vals='js:{"size": document.getElementById("lab-size-{s}").value}' hx-indicator="..."` |

### 8.2 Abas do lab (`showLabTab`)

Cada sub-aba tem `#lab-pane-{name}` + seletor `#lab-size-{name}` + botão **Run** + contêiner
`#lab-results-{name}`. O `hx-vals` com `js:` lê o valor atual do `<select>` na hora do click —
ou seja, o `size` é capturado dinamicamente sem listener extra.

JS ("todo" do front): `showTab`, `showLabTab`, `clearForm`, `showToast`.

### 8.3 CSS tokens (cyberpunk.css)

```css
--neon-cyan: #00f0ff;    --neon-magenta: #ff00ff;
--neon-green: #39ff14;   --neon-yellow: #f0ff00;
--bg-primary: #0a0a0f;   --bg-secondary: #12121a;
--text-primary: #e0e0e0; --text-secondary: #888;
--font-main: 'Inter', ...;  --font-mono: 'JetBrains Mono', ...;
```
Componentes-chave: `.card`, `.btn-primary/secondary/danger`, `.keys-table`,
`.badge-online/offline`, `.entropy-bar/.entropy-fill`, `.audit-grid/.audit-card`,
`.metric-row`, `.lab-tabs/.lab-tab-btn`, `.lab-table`, `.verdict-pass/.verdict-warn/.verdict-fail`,
`.toast`, `.htmx-indicator`, `.empty-state`, `.bg-grid`.

---

## 9. Docker e Deploy

**`docker-compose.yml`** — 3 serviços na rede `quantum-net`:
- `quantum-api` (:8081) — `Dockerfile.api`.
- `keymanager` / `quantum-keymanager` (:8082) — `Dockerfile.keymanager`; `MASTER_KEY_SECRET`
  hardcoded de dev (`super_secret_master_key_change_me_in_prod`) — **trocar em produção**.
- `rabbitmq` (3-management, :5672/:15672) — guest/guest, healthcheck `rabbitmq-diagnostics -q ping`.

Volumes: nenhum (SQLite in-memory).

**`Dockerfile.api` / `Dockerfile.keymanager`**: multi-stage.
- Builder `golang:1.25-alpine` instala `gcc musl-dev` (CGO p/ SQLite), `go mod download`,
  build `CGO_ENABLED=1`.
- Runtime `alpine:3.19` + `ca-certificates`; o keymanager copia o `web/static`.

> Requisito crítico: **CGO_ENABLED=1** por causa do driver `mattn/go-sqlite3`.

---

## 10. Fluxo End-to-End

```
1. Pool fica baixo (<200) após gerar/exportar chaves
2. svc.OnPoolLow() → scheduler.TriggerRefill() → refillChan (coalesce)
3. Goroutine do scheduler recebe sinal → collectEntropy()
   → GET quantum-api:8081 /api/v1/quantum-random?count=256&pure=true  (lote de 256 B)
   → quantum-api busca LfD (qrng?length=256&format=HEX) → hex→bytes
   → salva QuantumData{Source:"LFD"} → publica entropy.new → até 1000 registros
4. Usuário gera chave (UI) → POST /ui/keys → svc.GenerateKey(alias, 2048)
   → ConsumeEntropy(5) (transação: SELECT ... LIMIT 5 + UPDATE used=true)
   → buildQuantumSeed → xorReader × crypto/rand → rsa.GenerateKey
   → PEM pública/privada → aesGCMEncrypt(privPEM) → SaveKey
   → publica key.created → checkAndPublishPoolEvent()
5. Usuário exporta → POST /ui/keys/{id}/export → svc.ExportPrivateKey(id)
   → ConsumeEntropy(2) → aesGCMDecrypt → PEM → inline na UI (copy button)
   → publica key.exported → checkAndPublishPoolEvent()
```

---

## 11. Testes

```bash
go test ./...       # passa em todo o repo (sem testes em outros pacotes)
```

**`internal/audit/validators/validators_test.go`** é a suíte principal. Usa um **LCG**
próprio (`pseudoRandBytes`) + `allZeros` para gerar dados determinísticos.

| Teste | O que garante |
|-------|---------------|
| `TestIgamcReferenceValues` | Valores de referência: `Q(1,1)=e⁻¹`, `Q(2,2)=3e⁻²`, `Q(3,2)=5e⁻²`, **`Q(0.5,0.5)=erfc(√0.5)=0.3173105078629141`**, `Q(1,0)=1` (tol 1e-6); estabilidade para `a=2.5`. |
| `TestNormalCDF` | `Φ(0)=0.5`, `Φ(±1.96)`≈`0.975/0.025`. |
| `TestMonobit` | All-ones → p≈0; alternado → p≈1; aleatório → p∈[0,1]. |
| `TestRuns` | Alternado → p muito pequeno; aleatório → p∈[0,1]. |
| `TestBlockFrequency` | All-zeros (M=128) → fail; aleatório → p∈[0,1]. |
| `TestLongestRun` | All-zeros e all-ones → fail; aleatório → p∈[0,1]. |
| `TestApproximateEntropy` | All-zeros (m=5) → fail; aleatório → p∈[0,1]. |
| `TestSerial` | Aleatório: m adaptativo ∈[3,16], p1/p2 ∈[0,1]; all-zeros → ambos falham. |
| `TestCumulativeSums` | All-zeros → p=0 exato; aleatório → p∈[0,1] (fwd+rev). |
| **`TestCumulativeSumsDistribution`** | **Sanidade de distribuição**: 200 amostras aleatórias → falha deve ficar ~1% (α=0.01); bloqueava `fail ≤ 8` (pega "bateu 100% sempre" — o bug do sign do sum2). |
| **`TestLongestRunRandomDistribution`** | Idem para Longest Run (pega o bug dos bins/blocos fixos). |
| `TestMinEntropy` | All-zeros → ≈0; 1 MiB aleatório: MCV∈[7.9,8.0], bit≥0.999, MCV count≈4096, distinct≥250, `ExpectedDistinctValues(2^20)≥255`. |
| `TestStructure` | All-zeros: bias/auto muito altos, runs **+Inf**; aleatório: todos dentro dos limites. |

> **Por que esses dois testes de distribuição existem:** os bugs do Cumulative Sums e do
> Longest Run faziam p-valores degenerarem para ~0 em **todos** os dados (mesmo aleatórios).
> Um teste de "valor de referência" não pega isso; um teste de distribuição (~Uniform(0,1)
> com ~1% de falha a α=0.01 em 200 trials) detecta o colapso.

### Como rodar apenas um teste
```bash
go test ./internal/audit/validators/ -run TestCumulativeSumsDistribution -v
```

---

## 12. Convenções de Erros

- **Cadeia repository → service → handler.** Repositório devolve erro cru do GORM; o service
  embrulha com contexto (`fmt.Errorf("...: %w")`); o handler decide o status HTTP.
- **Comparação por string** em alguns handlers (ex.: `err.Error() == "key not found"` →
  404) — escolha deliberada (clara, embora frágil).
- **Sentinela** apenas em casos específicos: `ErrUnknownSuite` (lab) e
  `errors.Is(err, gorm.ErrRecordNotFound)` (not-found).
- **`FindKeyByID`** retorna `(nil, nil)` quando não acha — padrão "nil-safety".
- **Messaging opcional:** `publish()` é no-op se `s.pub == nil`; RabbitMQ indisponível só
  loga warn e segue.

---

## 13. Itens Futuros / TODOs

- **Consumidores RabbitMQ** — topologia pronta, mas nenhum consumer processa as filas ainda.
- `internal/audit/service.go:118` — `TODO: Fetch actual quantum data from repository`
  (hoje `getQuantumSample` itera `FindAllUnusedBySource`; evoluir para query eficiente por tamanho).
- `internal/collector/scheduler.go:159` — `TODO: Add NIST SP 800-90B entropy validation here`.
- Persistent storage (S3/arquivo) se houver requisito de sobrevivência de pool.
- `keymanager` não tem tratamento explícito de SIGTERM para o HTTP server (apenas
  `defer scheduler.Stop()`); algo a revisar.
- Segredo `MASTER_KEY_SECRET` hardcoded no compose — swap para secret manager em produção.

---

### Apêndice — bugs importantes já corrigidos (lições)

| Bug | Causa raiz | Correção |
|-----|-----------|----------|
| **Cumulative Sums (p≈0 sempre)** | termo `sum2` era subtraído (`1 − s1 − s2`) e bounds de `k` sem `/4` | `p = 1 − sum1 + sum2` + divisão inteira truncada, fiel ao `cusum.c` |
| **Longest Run (p≈0 sempre)** | usava todos os blocos + bins "range" | blocos fixos `N` (M=8/N=16, M=128/N=49) + bins pontuais (`≤V[0]`, `==V[j]`, `≥V[K]`) |
| **Fixed-point `igamc` divergente** | série iniciada de forma errada para `a` grande | `sum = 1/a` inicial na série |
| **`math.Lgamma`** | retorna 2 valores | `gln, _ := math.Lgamma(a)` |
| **Shifts de constante untyped** | erro de compilação em shift float64 | `one := int64(1)` tipado antes de shift |