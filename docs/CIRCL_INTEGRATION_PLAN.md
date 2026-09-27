# Plano de Integração — Cloudflare CIRCL (library de criptografia pós-quântica)

> **Status:** em espera — este é apenas o plano. A implementação só começa quando o
> dono do projeto sinalizar (o autor avisa quando estiver pronto).
>
> **Escopo deste documento:** decisões de arquitetura, fases de integração, impacto
> no código/documentação e critérios de aceite. Nenhum código é produzido aqui.

---

## 1. Contexto

O projeto `quantum-entropy-service-go` constrói material criptográfico a partir de
**entropia quântica** de fonte externa (LfD), valida a qualidade dessa entropia com
suítes de teste estatístico próprias (NIST STS, MinEntropy, estrutura) e usa o pool
resultante para gerar **chaves RSA** (`internal/keymanager/service.go`), protegidas
por **AES-256-GCM** com uma chave-mestra derivada de `MASTER_KEY_SECRET`.

O elo fraco disso tudo é o própria RSA: o algoritmo de Shor quebra fatoração em
máquina quântica suficientemente grande. Alimentar RSA com entropia quântica é
alinhado ao espírito do projeto, mas a *primitiva* RSA não é pós-quântica.

**O CIRCL (Cloudflare Interoperable, Reusable Cryptographic Library)** entrega o
padrão NIST pós-quântico FIPS em Go puro. A Cloudflare já roda híbrido
clássico+quântico por default em todas as bordas TLS desde 2022. É a opção madura
para deixar este serviço pós-quântico *sem* trocar o toolchain.

### 1.1 Por que CIRCL e não só a stdlib?

| Primitiva | Stdlib Go 1.25 | CIRCL v1.6.x |
|---|---|---|
| ML-KEM (FIPS 203, antes "Kyber") | `crypto/mlkem` (desde Go 1.24; 768/1024) | `kem/mlkem` (512/768/1024) |
| ML-DSA (FIPS 204, antes "Dilithium") | **não disponível** (só Go 1.27+) | `sign/mldsa` (44/65/87) |
| SLH-DSA (FIPS 205, antes "SPHINCS+") | não disponível | `sign/slhdsa` |
| X-Wing (híbrido X25519 + ML-KEM-768) | não disponível | `kem/xwing` |
| HPKE (RFC 9180) | não disponível | `hpke` (aceita híbridos) |
| Híbridos clássico+PQ genéricos | não disponível | `kem/hybrid` |
| SHA-3/SHAKE | `crypto/sha3` (desde Go 1.24) | `sha3` |
| X25519 / Ed25519 | `crypto/ecdh`, `crypto/ecdsa` | `dh/x25519`, `sign/ed25519` |

**Conclusão:** só de *key exchange* (ML-KEM) a stdlib em Go 1.25 já basta. O valor
adicionado do CIRCL para este projeto está em **ML-DSA, SLH-DSA, X-Wing e HPKE** —
que a stdlib 1.25 não oferece. Manter o `go 1.25` (Dockerfiles
`golang:1.25-alpine`) + CIRCL é mais barato do que subir toolchain para 1.27.

---

## 2. Inventário criptográfico atual (baseline a ser evoluído)

| Camada | Onde | Hoje | Futuro (CIRCL) |
|---|---|---|---|
| Geração de chaves | `internal/keymanager/service.go` (`GenerateKey`) | RSA-2048/4096, seed quântico via `xorReader` (`newXORReader`) + `rsa.GenerateKey` | + ML-KEM e ML-DSA com o **mesmo** padrão de seed quântico |
| Key wrapping | `service.go` (`aesGCMEncrypt/Decrypt`), `internal/keymanager/model.go` (`RsaKey.Nonce`) | AES-256-GCM, masterKey = SHA-256(`MASTER_KEY_SECRET`) | Envelope **HPKE pós-quântico** (X-Wing/ML-KEM) ou KEM direto |
| Persistência | `internal/keymanager/interfaces.go` (`KeyStore`), `model.go` | tabela `RsaKey` com PEM + nonce | coluna de algoritmo + campos de envelope; leitura retroativa |
| Eventos | `internal/messaging/events.go` | `KeyCreatedEvent`, `KeyExportedEvent.Algorithm="AES-256-GCM"` | campo `Algorithm`/`KeyType` nos eventos (JSON backward-compat) |
| Auditoria | `internal/audit/service.go`, `validators` | NIST STS próprios; `audit.complete` sem assinatura | relatório/atestação assinada (ML-DSA) |
| Transporte intraserviços | `cmd/*` (HTTP), `scheduler.go` (client LfD) | TLS clássico | híbrido TLS (baixa prioridade; ver Fase 4) |
| Entropia | `collector/scheduler.go` (pool 200/1000), `audit` | LfD + validators | **sem mudança** — CIRCL não é fonte de entropia |

---

## 3. Escopo (o que entra / o que fica fora)

### Entra
- Chaves pós-quânticas (ML-KEM e ML-DSA) ao lado do RSA no keymanager.
- Substituição/evolução do key wrapping para envelope híbrido pós-quântico (HPKE).
- Assinatura pós-quântica de artefatos de auditoria (ML-DSA; SLH-DSA opcional).
- (Opcional, low-prio) transporte HTTP híbrido entre serviços.

### Fora (não fazer)
- **Não** substituir os validators NIST STS próprios por CIRCL — o CIRCL *não*
  implementa STS; o lab de validação continua sendo o conjunto `internal/audit/validators`.
- **Não** usar esquemas pré-padrão aposentados: `kem/kyber`, `sign/dilithium`.
- **Não** tocar em SIDH/SIKE (`dh/sidh`, `kem/sike`) — primitivas isogenia
  **quebradas em 2022**; relatorias internas do CIRCL marcam como inseguras.
- **Não** usar a CIRCL como fonte de entropia; a entropia continua exclusivamente da LfD,
  validada pelo lab.
- **Não** migrar/regenerar chaves RSA existentes; elas continuam exportáveis (wrapping legado).

---

## 4. Fases de implementação

Cada fase tem **branch própria**, segue a convenção do repo (toda feature nova tem
testes; docs atualizados) e termina com `gofmt`, `go build`, `go vet`, `go test ./...` verdes.

### Fase 0 — Spike técnico (proof of concept, descartável)
Objetivo: medir e decidir parâmetros antes de tocar produto.

- `go get github.com/cloudflare/circl`.
- POC em branch de spike:
  - ML-KEM-768 (`kem/mlkem`): `GenerateKey` → `Encapsulate`/`Decapsulate` → roundtrip.
  - ML-DSA-65 (`sign/mldsa`): assinar/verificar.
  - X-Wing (`kem/xwing`) e HPKE suite híbrida (`hpke`).
  - Alimentar o seed com o mesmo `xorReader` quântico do keymanager (provar o conceito).
- Entregáveis: tabela de **tamanhos** (chaves/ciphertexts/assinaturas) e **tempo médio**;
  decisão dos conjuntos de parâmetros default (ver §6).
- Critério de saída: POC revisada; nada mergea (queda do spike).

### Fase 1 — Chaves pós-quânticas no keymanager (feature visível)
- Estender o modelo e o serviço para suportar múltiplos tipos de chave:
  - `internal/keymanager/model.go`: renomear/criar conceito `Key` com `Algorithm`
    (`rsa-2048`, `rsa-4096`, `mlkem-768`, `mlkem-1024`, `mldsa-65`, ...). Migração via
    `AutoMigrate` adicionando coluna com default — **não** quebrar a tabela `RsaKey`
    (compat com linhas existentes).
  - `service.go`: novo caminho `GeneratePQKey` (ou `Algorithm` no `GenerateKey`) usando
    CIRCL + seed quântico (`xorReader`); RSA permanece como fallback/compat.
  - Eventos: `KeyCreatedEvent` passa a carregar `Algorithm` (campo novo, opcional no JSON);
    `KeyExportedEvent.Algorithm` reflete o algoritmo real da chave.
  - UI (web): seletor de algoritmo; exibição do tipo por chave.
- Testes: roundtrip geração/persistência/exportação por tipo; evento correto;
  consumo de `entropyPerKey` mantido; chaves legadas intactas.

### Fase 2 — Key wrapping híbrido (HPKE / KEM envelope)
- Evoluir `aesGCMEncrypt/Decrypt` para um **envelope pós-quântico**:
  - Gera um envelope com a `MASTER_KEY_SECRET` mantida (compatível) **ou** novo caminho
    HPKE com suite híbrida (X-Wing `kem/xwing` + ChaCha20-Poly1305/AES-GCM).
  - Schema: novos campos em `Key` (ex.: `EncapsulatedKey`, `WrapVersion`); `Nonce` legado
    permanece lido via fallback (chaves antigas exportam com o fluxo AES-256-GCM atual).
- Justificativa: o RSA exportado de hoje é cifrado simétrico — adotar envelope
  híbrido deixa o *wrapping* alinhado à primitiva PQ, mantendo compat de leitura.
- Testes: roundtrip novo; regressão de exportação de chaves antigas; documentar tamanhos.

### Fase 3 — Assinatura pós-quântica de auditoria (ML-DSA, opcional SLH-DSA)
- Assinar o relatório `audit.complete` e/ou a atestação de entropia validada
  (`entropy.validated`) para rastreabilidade/anti-tamper.
- `internal/audit/service.go`: incluir `SignatureBase64` + `PublicKeyPEM` no `AuditReport`/
  evento; chave de assinatura do próprio serviço (ML-DSA-65 recomendado).
- Testes: verificação válida; rejeição de relatório adulterado.

### Fase 4 — Transporte híbrido entre serviços (opcional, baixa prioridade)
- Comunicação HTTP entre `quantum-api` e `keymanager` hoje sem PQ no TLS.
- Opção recomendada: **subir toolchain Go (1.26+/1.27)** para herdar o híbrido TLS
  default da stdlib (`SecP256r1MLKEM768`) em vez de costurar `tls.Config` com CIRCL.
- Se mantiver Go 1.25: usar CIRCL para agrupamento híbrido no `tls.Config` (mais código).
- Portanto, depende da decisão de toolchain (§6). Deixar para depois das Fases 1–3.

---

## 5. Impacto transversal (planejar cedo)

- **go.mod/go.sum:** dependência direta `github.com/cloudflare/circl` (BSD-3-Clause).
- **Dockerfile:** sem mudança estrutural — CIRCL é Go puro; `CGO_ENABLED=1` já usado
  (SQLite via gcc/musl-dev) não é afetado.
- **Schema DB (SQLite/GORM):** adicionar colunas com default para não quebrar registros
  existentes; manter leitura retroativa do formato atual.
- **Eventos RabbitMQ:** campos novos **opcionais** (JSON compat); atualizar a tabela de
  events em `PROJECT_DOCUMENTATION.md` e dar exemplo de payload novo.
- **UI/dashboard:** exibir `Algorithm` por chave; seleção na geração.
- **SQL no dashboard:** nada muda nas filas existentes (`entropy.*`, `key.*`, `pool.*`).

## 6. Decisões pendentes (endossar na hora da implementação)

1. **Toolchain:** manter Go 1.25 + CIRCL (recomendado) vs subir para 1.27 (ganha
   ML-DSA/ML-KEM na stdlib; troca os Dockerfiles para `golang:1.27-alpine`).
2. **Conjuntos de parâmetros default:** ML-KEM-768 (padrão de mercado) e ML-DSA-65
   (balance tamanho/segurança); SLH-DSA como opção "conservadora".
3. **Nome/modelo de dados:** manter tabela `RsaKey` com colunas novas vs novo modelo
   `Key` genérico. Recomendação: evoluir `RsaKey` → `Key` com `Algorithm`, já que
   `KeyStore` (interface) já isola o resto do sistema do schema.
4. **Wrapping:** HPKE (RFC 9180) híbrido X-Wing+ChaCha20 vs KEM direto (ML-KEM) + AES-GCM.
   HPKE é o caminho padrão moderno e encapsula isso.
5. **RNG:** manter o padrão `xorReader` (entropia quântica XOR `crypto/rand`) para o seed
   das chaves PQ, provando a premissa do projeto em todas as primitivas.

## 7. Riscos e mitigações

| Risco | Papel | Mitigação |
|---|---|---|
| Tamanhos grandes (ML-DSA pub 1952 B, sig 3309 B; SLH-DSA maior) | storage/eventos maiores | benchmark Fase 0; defaults ML-KEM-768/ML-DSA-65; documentar |
| Dependência externa | manter | pinar versão, KATs, acompanhar releases do CIRCL (v1.6.x ativo) |
| Chaves legadas | quebra de exportação | fallback de leitura por `WrapVersion`/algoritmo |
| Compat de eventos | consumidores antigos | campos novos opcionais (puramente aditivos) |
| Falsa sensação de segurança | primitiva errada | só padrões FIPS 203/204/205 finais; proibir pre-padrão e SIDH/SIKE |

## 8. Definição de Done (por fase)

- `gofmt` clean; `go build ./...`, `go vet ./...`, `go test ./...` verdes.
- Testes de roundtrip por primitiva + regressão de chaves legadas.
- Docs atualizados: `PROJECT_DOCUMENTATION.md` (seção cripto/new assinaturas),
  `PROGRESS_STATUS.md` (branch + bug/fix), `README.md` (stack/capabilidades).
- Branch mergeada por PR seguindo o fluxo do repo (`git_branches_control.txt`).

## 9. Próximos passos (disparados quando o autor sinalizar)

1. Confirmar as decisões da §6 (principalmente toolchain e parâmetros).
2. Branch `feat/circl-spike` → Fase 0 → revisar números e congelar defaults.
3. Branch `feat/pq-keys` → Fase 1 → PR com UI + eventos.
4. Branch `feat/hybrid-wrapping` → Fase 2 → PR.
5. Branch `feat/pq-audit-signature` → Fase 3 → PR.

## 10. Referências

- CIRCL: https://github.com/cloudflare/circl (v1.6.5, BSD-3-Clause)
- FIPS 203 (ML-KEM): https://doi.org/10.6028/NIST.FIPS.203
- FIPS 204 (ML-DSA): https://doi.org/10.6028/NIST.FIPS.204
- FIPS 205 (SLH-DSA): https://doi.org/10.6028/NIST.FIPS.205
- HPKE RFC 9180: https://www.rfc-editor.org/info/rfc9180
- Go stdlib `crypto/mlkem` (Go 1.24+); `crypto/mldsa` (Go 1.27+)
- Cloudflare tráfego pós-quântico (híbrido, desde out/2022)