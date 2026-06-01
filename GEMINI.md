# Quantum Entropy Service (Go) - Project Guidelines

## Project Overview
A microservices system for quantum entropy collection and cryptographic key management.

## Core Mandates
- **Language:** Go 1.25+
- **Architecture:** Microservices (Quantum API & Key Manager)
- **Messaging:** RabbitMQ (Asynchronous events)
- **Database:** SQLite (In-memory/Local) with GORM
- **Security:** NIST SP 800-90C for entropy mixing, RSA for key management.

## Project Structure
- `cmd/`: Entry points for services.
- `internal/`: Private library code.
  - `quantum/`: LfD API client and entropy mixing.
  - `keymanager/`: RSA key generation and storage (Missing).
  - `messaging/`: RabbitMQ abstractions.
  - `audit/`: Entropy validation logic.
  - `collector/`: Background entropy fetching.
- `web/`: HTMX + Templ frontend (Planned).

## Development Status (May 2026)
- [x] Initial structure and README.
- [x] Docker & Compose configuration.
- [x] Quantum entropy mixing logic.
- [x] RabbitMQ connection wrapper.
- [ ] **Next:** `cmd/quantum-api/main.go` implementation.
- [ ] **Next:** `internal/keymanager` implementation.
- [ ] **Next:** Database models and GORM setup.

## Technical Conventions
- Use `slog` for logging.
- Follow "Surgical Update" pattern for code changes.
- Ensure all new features have corresponding tests.
- Maintain Dockerfiles compatibility.
