# Progress Status — quantum-entropy-service-go
> Atualizado: 2026-09-07

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

---

## Fluxo Principal

```
[LfD Quantum API] ──HTTP──► quantum-api (:8081)
  GET /qrng?length=256&format=HEX
                                   │ scheduler (goroutine)
                                   │ coleta quando pool < 200
                                   │ para quando pool >= 1000
                                   │
                                   │ POST /api/v1/quantum-random
                                   ▼
                            keymanager (:8082)
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
keymanager: após gerar ou exportar chave, verifica pool
    pool < 200  → publica "entropy.pool.low"  → OnPoolLow() → scheduler.TriggerRefill()
    pool >= 1000 → publica "entropy.pool.ok"  → quantum-api loga estado saudável
```

---

## Eventos RabbitMQ

| Exchange | Routing Key | Evento | Publicado por |
|----------|-------------|--------|---------------|
| `entropy.collected` | `entropy.new` | `EntropyNewEvent` | `collector/scheduler.go` |
| `key.events` | `key.created` | `KeyCreatedEvent` | `keymanager/service.go` |
| `key.events` | `key.exported` | `KeyExportedEvent` | `keymanager/service.go` |
| `key.events` | `key.deleted` | `KeyDeletedEvent` | `keymanager/service.go` (via UI) |
| `audit.requests` | `audit.start` | `AuditStartEvent` | `audit/service.go` |
| `audit.results` | `audit.complete` | `AuditCompleteEvent` | `audit/service.go` |
| `entropy.pool` | `entropy.pool.low` | `PoolLowEvent` | `keymanager/service.go` |
| `entropy.pool` | `entropy.pool.ok` | `PoolOkEvent` | `keymanager/service.go` |

---

## Estado Atual — Implementado

| Pacote | Status | Observação |
|--------|--------|------------|
| `internal/quantum/` | ✅ | Cliente LfD + mixing NIST |
| `internal/keymanager/` | ✅ | CRUD RSA + AES-256-GCM wrap + publicação de eventos |
| `internal/messaging/` | ✅ | Topologia RabbitMQ completa |
| `internal/audit/` | ✅ | Shannon, Chi-Square, Monte Carlo + publicação de eventos + lab suites (`suites.go`, `validators/`) |
| `internal/audit/validators/` | ✅ | `igamc`, min-entropy (MCV+bits), NIST 800-22 subset, structure |
| `internal/collector/` | ✅ | Scheduler com hysteresis + TriggerRefill + publicação de eventos |
| `internal/ui/` | ✅ | Fragmentos HTMX + delete via Service + modal export fix + rota `/ui/lab` |
| `cmd/quantum-api/main.go` | ✅ | Entrypoint serviço 1 |
| `cmd/keymanager/main.go` | ✅ | Entrypoint serviço 2 + OnPoolLow wired |
| `web/static/` | ✅ | Frontend cyberpunk |
| Docker + Compose | ✅ | Containers configurados |

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

## TODOs Pendentes

- `internal/audit/service.go:118` — `TODO: Fetch actual quantum data from repository`
- `internal/collector/scheduler.go:159` — `TODO: Add NIST SP 800-90B entropy validation here`

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
