# Entropy Audit Lab — Expansão para Suítes Avançadas

> **Projeto:** `quantum-entropy-service-go`
> **Branch sugerida:** `feat/entropy-lab-suites`
> **Documento:** análise e plano de implementação
> **Última atualização:** 2026-09-07

---

## 1. Objetivo

Evoluir o **Entropy Audit Lab** existente, transformando o teste único básico em um
laboratório com **múltiplas suítes organizadas em abas**, mantendo intactos os testes
que já existem hoje e adicionando testes estatísticos avançados para realmente
diferenciar a qualidade de fontes de aleatoriedade (Quantum LFD vs CSPRNG vs PRNG).

O prompt-guia que motivou a análise aponta a lacuna central: os testes atuais não
conseguem separar um gerador quântico de um bom CSPRNG, e a pergunta do usuário é
exatamente **quais testes avançados são necessários e como apresentá-los no lab**.

---

## 2. Estado Atual (cenário "como está")

### 2.1 Backend
- **`internal/audit/service.go`** — `RunFullAudit(requestedSize)` (assinatura atual:
  `audit.NewService(repo, pub)`): gera amostras e executa `auditSource` para as 3 fontes:
  - Quantum LFD (via pool / `FindAllUnusedBySource`)
  - Java SecureRandom (CSPRNG) — `crypto/rand`
  - Java Random (LCRNG) — `math/rand`
  - Também publica `audit.start` / `audit.complete` via RabbitMQ.
- **`internal/collector/scheduler.go`** — `NewScheduler(repo, apiBaseURL, pub)`: coleta
  entropia do Quantum API e **publica `entropy.new`** a cada coleta.
- **`internal/audit/validators/validators.go`** — 5 métricas por fonte:
  | Métrica | Função |
  |---|---|
  | Shannon Entropy | `CalculateShannonEntropy` (~8.0 bits/byte) |
  | Chi-Square | `CalculateChiSquare` (~255 p/ 256 categorias) |
  | Pi Estimate | `EstimatePiMonteCarlo` (~3.1416) |
  | Compression Ratio | `CalculateCompressionRatio` (gzip, ~1.0) |
  | Repetitions | `CountRepetitions` (pares idênticos consecutivos) |
- **`internal/audit/handler.go`** — `GET /api/v1/quantum-entropy/audit?size=8192` (JSON).

### 2.2 UI (HTMX)
- **`internal/ui/handler.go:193`** — `runAudit`: gera fragmento HTML com o seletor
  `size`, renderiza `audit-grid` de `audit-card` (uma por fonte).
- O handler também expõe (desde o merge #6) `/ui/system-status` (health server-side) e
  `/ui/rabbitmq-queues` (proxy do Management API).
- Tamanho padrão: `8192` (8 KB). Opções atuais no front: 1 / 4 / 8 / 16 KB.

### 2.3 Frontend
- **`web/static/index.html`** — seção `#tab-entropylab`, seletor de tamanho (`#audit-size`)
  + botão **Run Audit** (`hx-get=/ui/audit`), contêiner `#audit-results`.
- **`web/static/css/cyberpunk.css`** — classes `.audit-grid`, `.audit-card`, `.metric-row`.
- Troca de páginas via `showTab(name, event)` (Dashboard / Key Vault / Entropy Lab / RabbitMQ).

### 2.4 Lacunas identificadas
- **Nenhum teste Go** no projeto (`*_test.go` inexistente).
- Testes só cobrem métricas "globais" (Shannon, chi-square) — insensíveis a padrões de
  ordem/correlação, não diferenciam Quantum de CSPRNG.
- PRNG não é semeado deterministicamente → auditoria não replicável entre execuções.
- ~~Bug pré-existente `/ui/service-health`~~ — **já corrigido** no merge #6
  (`/ui/system-status` server-side, resolve o problema de DNS do browser).

---

## 3. Decisões de Design (aprovadas)

| Tema | Decisão |
|---|---|
| Escopo NIST SP 800-22 | **Subconjunto prático (~9 testes)** em Go puro, com p-valor e veredito |
| Formato de resultados | **Tabela + veredito colorido** (`pass/warn/fail`) |
| Seletor de tamanho | **Ampliar opções até 256 KB** (limite do pool), mostrando mín. recomendado por suíte |

---

## 4. Plano de Implementação

### 4.1 Backend — `internal/audit`

#### a) Extração de amostras compartilhadas
- Gerar amostras **uma vez por execução** (Quantum LFD + CSPRNG + PRNG) e compartilhar
  entre as suítes.
- PRNG semeado deterministicamente (execuções replicáveis).

#### b) Modelo de resultado
- `MetricRow { Métrica, Valor (float64), Unidade, Limite/Ref, Veredito (pass/warn/fail), Comentário }`
- `Suite { ID, Nome, Descrição, MinRecomendadaBytes, Run(samples) → []SourceResult }`
- `SourceResult { Fonte, Rows []MetricRow }`

#### c) Suítes

| Aba/ID | Testes | Tamanho mínimo recomendado |
|---|---|---|
| **basic** (mantida, padrão) | Shannon, Chi-Square, Pi MC, Compression, Repetitions **+ vereditos automáticos** | 1–16 KB |
| **min-entropy** | Estimador 8-bit (MCV com correção SP 800-90B) + min-entropy/bit + independência de bits | ≥ 1M amostras (~1 MB); **indicativo** abaixo |
| **nist** | Subset SP 800-22: Monobit, Block Frequency (M=128), Runs, Longest Run of Ones, Serial (m=16), Approximate Entropy (m=5), Cumulative Sums (fwd/rev) | ≥ 125 KB (1M bits); **indicativo** abaixo |
| **structure** | Bit bias por posição, autocorrelação (lags 1–16), runs z-score, correlação serial | ≥ 64 KB |

> **Nota de limitação real:** o pool quântico = até 1000 registros × 256 B ≈ **256 KB**.
> As suítes avançadas rodam em regime **indicativo** nesse tamanho para Quantum, mas
> CSPRNG/PRNG podem ser avaliados com amostras maiores.

> **Adaptações de viabilidade (aplicadas nos testes NIST, fora dos mínimos oficiais):**
> - **Serial**: o padrão exige `m=16` com `n ≥ 2·m·2^m ≈ 2M bits` (acima do pool). Usar
>   **`m` adaptativo** — maior `m` tal que `2·m·2^m ≤ n` — com rótulo "indicativo".
> - **Longest Run of Ones**: seleção de bloco por faixa de tamanho da amostra
>   (tiers do padrão), documentada na UI.
> - Tudo abaixo dos mínimos oficiais é exibido como **indicativo**, nunca como
>   aprovação/reprovação formal.

#### d) Compatibilidade retida
- `RunFullAudit` e `GET /api/v1/quantum-entropy/audit` **permanecem intactos**
  (README/curl continuam válidos).

#### e) Testes Go
- **`validators_test.go`**: data-driven com
  - dados cínicos (todos zeros → Shannon 0, min-entropy 0)
  - dados ~uniformes → passam nos limites
  - p-valores de referência dos testes NIST.

### 4.2 UI — `internal/ui/handler.go`
- Nova rota `GET /ui/lab?suite=basic|min-entropy|nist|structure` → fragmento HTMX,
  no padrão atual, com cabeçalho (descrição da suíte, tamanho usado, mín. recomendado)
  + **tabela com colunas** `métrica / valor / veredito` colorido.

### 4.3 Frontend — `index.html` + `cyberpunk.css`
- Barra de abas **aninhada dentro do Entropy Lab** (`showLabTab(name)`, painéis `lab-pane-*`),
  espelhando o `showTab` atual.
- 4 abas, cada uma com seu seletor de tamanho + botão Run + contêiner de resultados.
- Opções de tamanho ampliadas (até 256 KB) + dica de mínimo por suíte.
- Novas classes: `.lab-tab-btn`, `.verdict-pass`, `.verdict-warn`, `.verdict-fail`
  (verde / âmbar / vermelho).

### 4.4 Validação
- `go build ./...` e `go vet ./...`
- `go test ./...`
- Smoke test dos 4 fragmentos via HTMX no container.

---

## 5. Resumo das Melhorias

| Ponto | De | Para |
|---|---|---|
| Espectro de testes | Só 5 métricas globais | Suítes básico + min-entropy + NIST 800-22 + estrutura |
| Diferenciação Quantum vs CSPRNG | Impossível (resultados quase idênticos) | viável com testes de ordem/correlação |
| Resultados | `audit-card` sem veredito | Tabela com veredito colorido (pass/warn/fail) |
| Replicabilidade | PRNG sem semente fixa | PRNG deterministicamente semeado |
| Cobertura de código | Nenhum teste Go | `validators_test.go` com casos limite e referências |
| Navegação | Tela única de audit | Múltiplas abas dentro do lab |

---

## 6. Notas / Itens de follow-up
- ~~Bug de rota inexistente `/ui/service-health`~~ — **corrigido** no merge #6
  (`/ui/system-status` server-side). Não blocker.
- ~~Emissão de eventos RabbitMQ~~ — **concluída**: `key.created/exported/deleted`
  (merge #7) e `entropy.new` + `audit.start/complete` (merge #8). O **consumer**
  dedicado para processar as filas (Option A) permanece fora de escopo — futuro.
- Considerar `GIN_MODE=release` no `docker-compose.yml` para logs menos verbosos.