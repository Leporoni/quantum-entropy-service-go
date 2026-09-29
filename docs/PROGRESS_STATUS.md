# Progress Status — quantum-entropy-service-go
> Atualizado: 2026-09-27

---

## Resumo do Projeto

Reescrita em Go do `quantum-entropy-service` (Java/Spring Boot). Coleta entropia quântica real da LfD Quantum API, armazena em pool, e usa para gerar chaves RSA criptograficamente superiores. Comunicação entre serviços 100% via RabbitMQ.

---

## Stack Tecnológica

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

## Branches de Implementação

| Branch | Escopo | Status |
|--------|--------|--------|
| `main` | Scaffold inicial + ajustes de versão | ✅ Feito |
| `feat/keymanager-core` | `internal/keymanager/` completo (model, repository, service, handler) | ✅ Feito |
| `feat/entrypoints` | `cmd/quantum-api/main.go` + `cmd/keymanager/main.go` + fix `quantum/handler.go` | ✅ Feito |
| `feat/event-driven` | Eventos RabbitMQ `pool.low`/`pool.ok` + hysteresis no scheduler | ✅ Feito |
| `feat/web` | Frontend cyberpunk HTML + HTMX + `internal/ui/` | ✅ Feito |
| `feat/ui-rabbitmq-dashboard` | Tab RabbitMQ no frontend + fix system status server-side | ✅ Feito |
| `feat/rabbitmq-events-dashboard` | Publicação de eventos de chave + wire pool refill | ✅ Feito |
| `feat/rabbitmq-events-and-ui-fixes` | Publicação de todos os eventos + fix modal export + fix delete UI | ✅ Feito |
| `feat/entropy-lab-suites` | 4 suítes do Entropy Audit Lab (basic, min-entropy, nist, structure) + `/ui/lab` | ✅ Feito |
| `fix/dockerfile-remove-go-mod-tidy` | Remove `RUN go mod tidy` do build Docker (falha DNS de deps de teste) | ✅ Feito |
| `feat/audit-events-for-lab-suites` | Publica `audit.start`/`audit.complete` quando suites do Entropy Lab rodam | ✅ Feito |
| `feat/waitgroup-graceful-shutdown` | Graceful shutdown no quantum-api com `sync.WaitGroup` | ✅ Feito |
| `feat/interfaces-abstraction` | Injeção de `EntropyStore`/`KeyStore`/`EventPublisher` no lugar de tipos concretos | ✅ Feito |
| `feat/panic-recover-middleware` | Recovery custom (`defer`/`recover`) + canário `/debug/panic` | ✅ Feito |
| `fix/pool-ok-events-and-docs` | Corrige `pool.ok` (agora publicado pelo scheduler no fim do refill) + docs pos-interfaces | em andamento |
| `feat/scheduler-fanout-pool` | Refill do scheduler com **fan-out** (worker pool: canais + `sync.WaitGroup` + `atomic.Int64`), split do chunk e semáforo na LfD | ✅ Feito |
| `feat/sync-primitives-mutex-once` | **`sync.Mutex`** no `xorReader` e no `Connection` + **`sync.Once`** na derivação da chave AES (`keymanager`); **lazy connect** (cache negativo), liveness de canal, eliminação do `pub == nil` | ✅ Feito |

---

## Fluxo Principal

```
[LfD Quantum API] ──HTTP──► quantum-api (:8081)
  GET /qrng?length=1024&format=HEX
                                    │ scheduler (goroutine)
                                    │ coleta quando pool < 200
                                    │ para quando pool >= 1000
                                    │
                                    │ GET /api/v1/quantum-random?count=1024&pure=true
                                    ▼
                             keymanager (:8082)
                        split 1024 B → 4 registros de 256 B
                        fan-out: 4 workers, 1 writer no SQLite
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

---

## Hysteresis Event-Driven

```
keymanager: após gerar ou exportar chave (checkPoolStatus)
    pool < 200 → OnPoolLow() → scheduler.TriggerRefill()  (refill local, sem depender de RabbitMQ)
               → publica "entropy.pool.low"
scheduler: ao fim do refill (collectEntropy)
    pool >= 1000 → publica "entropy.pool.ok"  (única fonte do "pool saudável")
```

---

## Eventos RabbitMQ

| Exchange | Routing Key | Evento | Publicado por |
|----------|-------------|--------|---------------|
| `entropy.collected` | `entropy.new` | `EntropyNewEvent` | `collector/scheduler.go` |
| `entropy.collected` | `entropy.validated` | `EntropyValidatedEvent` | `collector/scheduler.go` |
| `key.events` | `key.created` | `KeyCreatedEvent` | `keymanager/service.go` |
| `key.events` | `key.exported` | `KeyExportedEvent` | `keymanager/service.go` |
| `key.events` | `key.deleted` | `KeyDeletedEvent` | `keymanager/service.go` (via UI) |
| `audit.requests` | `audit.start` | `AuditStartEvent` | `audit/service.go` + `audit/suites.go` (só se o pool tiver dado) |
| `audit.results` | `audit.complete` | `AuditCompleteEvent` | `audit/service.go` + `audit/suites.go` (só se o pool tiver dado) |
| `entropy.pool` | `entropy.pool.low` | `PoolLowEvent` | `keymanager/service.go` (pós-consumo) |
| `entropy.pool` | `entropy.pool.ok` | `PoolOkEvent` | `collector/scheduler.go` (fim do refill, pool ≥ 1000) |

---

## Estado Atual — Implementado

| Pacote | Status | Observação |
|--------|--------|------------|
| `internal/quantum/` | ✅ | Cliente LfD + mixing NIST |
| `internal/keymanager/` | ✅ | CRUD RSA + AES-256-GCM wrap + publicação de eventos |
| `internal/messaging/` | ✅ | Topologia RabbitMQ completa |
| `internal/audit/` | ✅ | Shannon, Chi-Square, Monte Carlo + publicação de eventos + lab suites (`suites.go`, `validators/`) |
| `internal/audit/validators/` | ✅ | `igamc`, min-entropy (MCV+bits), NIST 800-22 subset, structure |
| `internal/collector/` | ✅ | Scheduler com hysteresis + TriggerRefill + publicação de eventos + refill fan-out (4 workers, split 1024→4×256 B) |
| `internal/ui/` | ✅ | Fragmentos HTMX + delete via Service + modal export fix + rota `/ui/lab` |
| `cmd/quantum-api/main.go` | ✅ | Entrypoint serviço 1 |
| `cmd/keymanager/main.go` | ✅ | Entrypoint serviço 2 + OnPoolLow wired |
| `web/static/` | ✅ | Frontend cyberpunk |
| Docker + Compose | ✅ | Containers configurados |

---

## Refill do Scheduler — Fan-Out (worker pool)

Branch `feat/scheduler-fanout-pool`. O refill deixou de ser um fetch sequencial de 256 B com `time.Sleep(200ms)`.

**Split do chunk** — cada fetch pede o máximo do endpoint (`count=1024`, `== quantum.MaxCount`) e `saveRecords` fatia em registros de 256 B (`entropyChunkBytes/entropyRecordBytes = 4`). O tamanho do registro **não** mudou de propósito: as watermarks (200/1000) e o consumo por chave contam **registros**, não bytes — trocar para 1024 B por registro inflaria o pool 4× e quebraria a semântica do hysteresis.

**Fan-out / fan-in** (`runRefillBatch`, `internal/collector/scheduler.go:146-204`):
- 4 workers (`refillWorkers`) leem de um canal `jobs` **não-bufferizado** — cada job sai uma única vez, e o produtor aborta cedo via `select` no `stopChan`
- Produtor cede assim que `failCount.Load() >= maxFailures` (`atomic.Int64`, decisão compartilhada com os workers)
- Goroutine "closer" separada: `wg.Wait()` → `close(results)` (evita `send on closed channel`)
- **Single-writer:** só o coletor chama `SaveEntropy`. O SQLite in-memory rejeita escritores concorrentes — saves paralelos dariam `database is locked`

**Semáforo na LfD** (`internal/quantum/client_lfd.go:20,47-48`): `sem chan struct{}` com `maxConcurrentLfd = 4` limita os requests simultâneos ao dispositivo físico — teto global, não só do scheduler.

- **Problema:** refill sequencial, 1 request de 256 B por vez com `time.Sleep(200ms)` hardcoded
- **Correção:** 4 fetches concorrentes de 1024 B, split em 4 registros cada, backoff configurável (`retryDelay`, default 1 s) e `maxFailures = 10` como teto de erro
- **Status:** ✅ Corrigido (branch `feat/scheduler-fanout-pool`)

---

## Primitivas `sync` — Mutex, Once e o Lazy Connect

Branch `feat/sync-primitives-mutex-once`. Fecha o item 13 do checklist Go: com esta
branch as **quatro** primitivas `sync` têm ocorrência real no projeto — `WaitGroup`
(graceful shutdown + fan-in + fan-out), `atomic` (circuit breaker do refill),
`Mutex` (desta branch) e `Once` (desta branch, **no `keymanager`**).

### Por que a topologia AMQP **não** usa `sync.Once`

Esta é a lição mais cara do diff, e o registro canônico do que a branch entregou.

A primeira versão do diff usava `sync.Once` para disparar a declaração da topologia.
Foi revertida, e o motivo é duplo:

1. **`Once` gasta o disparo mesmo quando `declareTopology()` retorna erro.** Uma falha
   transitória — PRECONDITION_FAILED numa fila cujos argumentos divergem da visão do
   broker, o broker ainda subindo, o canal ainda negociando — fixava `topologyErr`
   para sempre. O processo inteiro ficava sem topologia, sem erro novo, sem retry:
   o `Once` já havia sido consumido.
2. **Um `Once` gasto não pode ser desfeito quando o canal muda.** `connectLocked`
   substitui o canal a cada reconnect, e canal novo nasce sem exchange e sem fila.
   Com o `Once` já consumido pela falha inicial, o canal reinstallado ficava
   permanentemente sem topologia, e **todo `Publish` depois de um restart do broker
   levava `404 NOT_FOUND`** — para sempre, sem redial, sem redéclaro.

O que o código faz hoje: `topologyDeclared bool` sob `c.mu`
(`internal/messaging/connection.go:75`). `Channel()` marca `true` **só depois** de
`c.topology()` retornar `nil`, então falha não gruda; e tanto o redial quanto a
reabertura de canal zeram o flag, então cada canal novo declara a sua. Isso é
exatamente a dupla condição que o `Once` não consegue expressar.

**O `sync.Once` real da branch está em `internal/keymanager/service.go:73-79`**
(`masterKey`), derivando a chave AES-256. Ali ele é apropriado: a derivação é
função pura de um segredo imutável, portanto não pode falhar transitoriamente e não
tem o que ser tentado de novo. `TestMasterKeyDerivedOnce` usa um `Service` **frio**
na fase concorrente justamente para provar isso — com um `Service` já aquecido, uma
troca por `if s.key == nil` sem sincronização passa despercebida.

### `sync.Mutex` no `xorReader` (`internal/keymanager/service.go:300-329`)

O lock cobre **só o laço XOR** (`:321-326`) — `rand.Reader.Read` fica de fora, para
o CSPRNG continuar paralelo — e o `if len(x.seed) == 0` (`:315-317`) retorna antes
de tomar o lock. Hoje **não há race** (o reader nasce e morre dentro de
`GenerateKey`): o lock existe para blindar a primitiva, que o CIRCL vai reutilizar
como seed de ML-KEM/ML-DSA, cuja geração pode consumir o reader em paralelo
(`docs/CIRCL_INTEGRATION_PLAN.md:95,163`).

A guarda de seed vazio não é decorativa: `buildQuantumSeed` (`:283-292`) devolve
`nil` para lista vazia **ou base64 corrompido**, `newXORReader(nil)` (`:306`) é o
caminho real, e sem a guarda o laço faz `x.offset % len(x.seed)` e entra em
`panic: integer divide by zero` — dentro da geração de chave, exatamente no
momento em que o pool voltou vazio.

### Lazy connect (`internal/messaging/connection.go`)

Antes o `main` dialava na construção com **5 tentativas e backoff**
(`time.Sleep((i+1)*2s)`, ~30 s no total) e, se falhasse, o processo seguia **sem
mensageria para sempre**. Agora:

- `NewConnection(url)` (`:94-96`) **não toca a rede** — só guarda a URL, a função
  `dial` (seam de teste) e `reconnectDelay = 5s` (`:101-105`).
- `Channel()` (`:114-127`) diala no primeiro uso, sob `c.mu`, e dispara a
  topologia enquanto `topologyDeclared` estiver `false`.
- `connectLocked` (`:153-195`) faz **uma única tentativa** — o retry vem de graça
  dos chamadores (o scheduler faz tick a cada 5 s; cada publish tenta de novo). Um
  `Publish` que esperasse 30 s bloquearia um request HTTP.
- **Cache negativo** (`lastDialFail`/`lastDialErr`/`reconnectDelay`, lidos por
  `cachedDialError` em `:236-242`): impedem que cada publish dispare um dial
  enquanto o broker está fora. Sucesso limpa o cache (`:192`), e o `Close()` também
  (`:267`), senão um par `Close()`/`Channel()` se recusaria a dialar por até
  `reconnectDelay` sem motivo.
- **Liveness de canal, não só de sessão** (`:154-159` + `reopenChannelLocked`
  `:200-231`): o broker mata o **canal**, não a conexão, quando uma declaração é
  recusada com PRECONDITION_FAILED. Checando só `session.IsClosed()`, o canal morto
  continuava sendo devolvido: todo `Publish` posterior devolvia `amqp.ErrClosed`
  para sempre, sem redial, sem topologia e **sem erro do lado do `Channel()`**. A
  escolha é **reabrir canal na sessão viva** (um round trip) em vez de redialar
  (TCP + handshake + auth): a sessão viva por definição já passou pelo handshake, e
  é justamente no rastro de um restart storm que o dial caro mais dói.
- `Close()` (`:259-287`) **devolve `error`** e **libera `c.mu` antes do round trip**:
  o handshake de close do AMQP não tem timeout próprio, e segurar o lock travaria
  todo `Publish` contra um broker travado. `TestCloseDoesNotHoldTheLockDuringTheCloseHandshake`
  (`connection_test.go:736`) prova isso com um `Close()` que bloqueia de propósito.
- `declareTopology` (privado, `internal/messaging/consumer.go:92-135`) era o
  `SetupExchangesAndQueues` exportado, chamado sequencialmente no `main`. No caminho
  lazy ele é alcançado **concorrentemente pelos handlers HTTP do Gin** (uma
  goroutine por request) e pelo path de `audit` — nunca pelos workers do refill:
  eles só fazem `s.fetch()` (HTTP) e empurram para `results`; o laço single-writer de
  `saveRecords` (`internal/collector/scheduler.go:249`) chama `publishEntropyEvents`
  (`:270`, a partir de `:265`), e ele roda em uma goroutine só.
- `Channel()` passou a devolver `(*amqp.Channel, error)`; os 3 call sites
  (`publisher.go:43`, `consumer.go:24` e `:68`) trataram o erro.

**Efeito colateral: `pub` nunca é mais `nil`.** `cmd/keymanager/main.go` agora faz
`pub := messaging.NewPublisher(mqConn)` incondicionalmente. Os guards
`if s.pub == nil` em `keymanager/service.go`, `audit/service.go`, `audit/suites.go` e
`collector/scheduler.go` foram **mantidos** — `nil` continua válido para `NewService`
em testes —, mas no caminho real eles deixaram de ser exercitados.

### Testes e cobertura desta branch

`internal/messaging/connection_test.go` — **primeiro arquivo de teste do pacote**,
**19 funções `Test` (22 casos, com os 3 subtestes do cache negativo)**:
conexão lazy, cache negativo, retry de topologia, rollback de canal, sessão/canal
nil, reconnect com troca de sessão, reabertura de canal morto, `Close()` concorrente
com `Channel()`, `Close()` sem segurar o lock, `Publish`/`Consume` propagando erro de
canal e os dois construtores. `internal/keymanager/service_test.go` ganhou
`TestXORReaderWithoutSeedReadsFromRand`, a reescrita de `TestMasterKeyDerivedOnce`
(agora com `Service` frio na fase concorrente) e
`TestNewServiceDoesNotDeriveTheKeyEagerly`.

**O que ficou para trás, de propósito.** Os round trips AMQP **não têm cobertura**:
`*amqp.Channel` é um tipo concreto e a seam `amqpSession` para na conexão, então nada
abaixo de `Channel()` é alcançável por um teste unitário. A 0%:
`declareTopology`, `DeclareExchange`, `DeclareQueue`, `DeclareDeadLetterExchange`.
Abertos, mas nunca alcançados: o caminho de sucesso de `Publish`, de `Consume` e de
`Qos`, e o round trip do `Close()` (`Close` está em 87,5% — a parte que fecha as
coisas é o que não tem teste). Não existe suíte de integração nem build tag que
cubra isso: o único alvo do compose para o RabbitMQ é um healthcheck
`rabbitmq-diagnostics -q ping`. A interface rasa `amqpChannel` que tornaria isso
testável foi **adiada por decisão**, não esquecida — e o custo dela está medido: é o
motivo de `newConnectedTestConnection` não registrar `t.Cleanup` (o `Close()` de um
`&amqp.Channel{}` zerado dá panic dentro do cliente AMQP), e portanto o motivo de
`Close()` não ter teste próprio.

Cobertura do package: **66,9%**, partindo de **0%** — em `89349ab` o package
`internal/messaging` não tinha arquivo de teste nenhum.

`go test -race ./...`, `go test -race -count=50 ./internal/messaging/ ./internal/keymanager/`
e `go test -race -cpu=1,2,8 -count=20 ./internal/messaging/ ./internal/keymanager/`
limpos.

---

## Bugs Corrigidos

### UI Handler — delete bypassa Service (sem eventos)
- **Problema:** `ui/handler.go:146,158` chamava `repo.DeleteAllKeys()` e `repo.DeleteKeyByID()` diretamente
- **Correção:** Trocado por `h.svc.DeleteAllKeys()` e `h.svc.DeleteKey(id)`
- **Status:** ✅ Corrigido

### Modal de export persistia após delete
- **Problema:** Ao exportar uma chave e depois deletá-la, o modal com o PEM permanecia visível
- **Causa:** `hx-target="closest tr"` removia apenas a linha de dados, não a linha do modal
- **Correção:** Cada chave agora envolta em `<tbody id="key-row-{id}">`, delete targeta o `<tbody>` inteiro
- **Status:** ✅ Corrigido

### Events not published to RabbitMQ
- **Problema:** `EntropyNewEvent`, `AuditStartEvent`, `AuditCompleteEvent` nunca eram publicados
- **Causa:** `collector.Scheduler` e `audit.Service` não tinham `*messaging.Publisher`
- **Correção:** Publisher injetado em ambos, eventos publicados nos pontos corretos
- **Status:** ✅ Corrigido

### NIST Longest Run of Ones — distribuição degenerada
- **Problema:** p-valor ~0 para qualquer entrada; bins mapeados errados contra o spec
- **Causa:** usava **todos** os blocos do sample com bins "range" (NIST usa **número fixo** de blocos `N` sobre os primeiros `N·M` bits e bins pontuais: bin 0 = run ≤ V[0], bin K = run ≥ V[K])
- **Correção:** blocos fixos (M=8/N=16, M=128/N=49) + mapeamento de bins fiel ao spec
- **Status:** ✅ Corrigido

### NIST Cumulative Sums — p-valor ~0 em 100% dos dados aleatórios
- **Problema:** `NISTCumulativeSums` retornava p≈0 (às vezes ligeiramente negativo) para qualquer amostra
- **Causa:** sinal errado no termo `sum2` (`p = 1 − sum1 − sum2`) e limites de `k` sem divisão por 4; divergência do `cusum.c` de referência do STS 2.1a
- **Correção:** `p = 1 − sum1 + sum2`, bounds `(±n/z ± 1)/4` com divisão inteira truncada (C), e `zrev` próprio para a direção reversa
- **Status:** ✅ Corrigido

### `entropy.pool.ok` nunca aparecia no dashboard RabbitMQ
- **Problema:** a fila `q.pool.ok` ficava sempre zerada enquanto as demais marcavam normalmente
- **Causa:** o `pool.ok` era publicado pelo `checkAndPublishPoolEvent` do keymanager num branch **estruturemente inalcançável** — o pool nunca passa de 1000 (`highWatermark`) e a checagem roda **após** o consumo de entropia, então `count >= 1000` jamais era satisfeito
- **Correção:** `pool.ok` passou a ser publicado pelo **scheduler** ao fim do refill, quando `count >= 1000`. `OnPoolLow` saiu do guard `pub == nil` (refill local agora funciona mesmo sem RabbitMQ)
- **Status:** ✅ Corrigido (branch `fix/pool-ok-events-and-docs`)

---

## Entropy Audit Lab

Consulta determinística (PRNG com seed fixo) e descritivo dos 4 testes no frontend:

| Suíte | Mínimo recomendado | Aba | Verificação (α=0.01) |
|-------|--------------------|-----|----------------------|
| Basic | 8 KB | `basic` | Shannon, Chi-Square, Pi, Compression, Repetitions |
| Min-Entropy | 1 MB | `min-entropy` | MCV (most common value), bit min-entropy | 
| NIST SP 800-22 | 125 KB | `nist` | Monobit, Block Freq, Runs, Longest Run, Approx Entropy, Serial, Cumulative Sums (fwd+rev) |
| Structure | 64 KB | `structure` | Bias, Autocorrelation, Runs z-score, Serial correlation |

- `STANDARD: audit.RunSuites(suite, size, seed)` com registry `suiteDef` (nome, descrição, `minBytes`, runner) + `ErrUnknownSuite`
- PRNG determinístico: `math/rand` seedado com `DefaultPRNGSeed` (=12345), reproducível por `?seed=` na URL
- Verdicts `pass`/`warn`/`fail`; Serial usa `m` adaptativo ∈ [3,16] com `2·m·2^m ≤ n`
- Abaixo do mínimo: banner "indicative" em vez de pass/fail formal
- UI: tamanho por aba (até 256 KB) via `hx-get="/ui/lab?suite=..."`; `basic` mantém cards, demais usam `lab-table`
- `RunFullAudit` e `GET /api/v1/quantum-entropy/audit` mantidos intactos; `getPrngSample(size, seed)` novo em `service.go`

---

## Auditoria com Pool Vazio — Sem Eventos Fantasma

- **Problema:** `RunFullAudit` publicava `audit.start` **antes** de olhar o pool, e `audit.complete`
  era publicado mesmo com `results == nil`. Bastava abrir `/ui/audit` ou `/ui/lab` no navegador
  (fragmento htmx, polling) para gerar os dois eventos com o pool ainda vazio.
- **Fix:** as duas funções adquirem a amostra `Quantum (LFD)` **antes** de qualquer `Publish`.
  Pool vazio → `slog.Warn` + retorno de `audit.ErrNoQuantumData`, **zero eventos** publicados.
- **Ordem preservada em `RunSuites`:** a resolução da `suiteDef` continua antes da checagem de pool,
  então `ErrUnknownSuite` não é mascarado por `ErrNoQuantumData`.
- **UX:** `ui/handler.go` trata `errors.Is(err, audit.ErrNoQuantumData)` e mantém a mensagem
  amigável "No quantum data in pool yet. Wait for pool to fill.".
- **Cobertura:** `internal/audit/service_test.go` (fake publisher que conta publicações por
  routing key): 3 testes de pool vazio/suite inválida e 2 de pool cheio (1 `audit.start` +
  1 `audit.complete`).
- **Status:** ✅ Corrigido

---

## Docker Build — Correção de Falha

- **Problema:** `RUN go mod tidy` no build falhava com `lookup proxy.golang.org: no such host`
- **Causa:** O `tidy` tenta baixar deps de teste transitivas de libs de terceiros (testify, goleak, mock, go-cmp) que não estão no `go.sum` — blob de rede bloqueado no container
- **Fix:** Removido `RUN go mod tidy` de `Dockerfile.api` e `Dockerfile.keymanager`; `go.sum` já está completo (verificado com `go mod verify` + `go build ./...`)
- **Status:** ✅ Corrigido (branch `fix/dockerfile-remove-go-mod-tidy`)

---

## Docker Runtime — DNS do Container (WSL2)

- **Problema:** `GET /api/v1/quantum-random?count=1024&pure=true` retornava 500 em ~0,030 s com
  `dial tcp: lookup lfdr.de on 127.0.0.11:53: no such host`. Não tinha relação com o fan-out do
  scheduler: o pool nunca enchia, antes ou depois dele.
- **Causa:** o host é **WSL2**, cujo `/etc/resolv.conf` aponta para `10.255.255.254` — endereço
  inalcançável a partir do NAT do container. O resolver embutido do Docker (`127.0.0.11`) forwarda
  para esse nameserver e falha. O `network: host` do `docker-compose.yml` está dentro de `build:`,
  então vale só para o build, **não** em runtime.
- **Fix:** pin de resolvers públicos em `quantum-api` (`dns: [8.8.8.8, 1.1.1.1]`). Aplicado apenas
  nesse serviço: o `keymanager` resolve `quantum-api` pelo DNS embutido do Docker e não precisa de
  DNS externo.
- **Diagnóstico:** a própria LfD está saudável (200 para 256 B e 1024 B, single e 4× concorrente) —
  o erro é puramente de resolução de nome dentro do container.
- **Status:** ✅ Corrigido

---

## TODOs Pendentes

- `internal/audit/service.go:123` — `TODO: Fetch actual quantum data from repository`
- `internal/collector/scheduler.go:241` — `TODO: Add NIST SP 800-90B entropy validation here`

---

## Variáveis de Ambiente

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

## Endpoints de Acesso

| Serviço | URL |
|---------|-----|
| Frontend UI | http://localhost:8082 |
| Quantum API | http://localhost:8081 |
| Key Manager API | http://localhost:8082/api/v1 |
| RabbitMQ Management | http://localhost:15672 (guest/guest) |

---

## Convenções Técnicas

- Usar `slog` para logging
- Pattern "Surgical Update" para mudanças de código
- Todo feature nova deve ter testes correspondentes
- Manter compatibilidade dos Dockerfiles
