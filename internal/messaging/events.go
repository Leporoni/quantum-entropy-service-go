package messaging

import "time"

// Exchange names
const (
	ExchangeEntropyCollected = "entropy.collected"
	ExchangeKeyEvents        = "key.events"
	ExchangeAuditRequests    = "audit.requests"
	ExchangeAuditResults     = "audit.results"
)

// Routing keys
const (
	RoutingKeyEntropyNew       = "entropy.new"
	RoutingKeyEntropyValidated = "entropy.validated"
	RoutingKeyKeyCreated       = "key.created"
	RoutingKeyKeyExported      = "key.exported"
	RoutingKeyKeyDeleted       = "key.deleted"
	RoutingKeyAuditStart       = "audit.start"
	RoutingKeyAuditComplete    = "audit.complete"
)

// --- Event Payloads ---

// EntropyNewEvent is published when new quantum entropy is fetched from the API.
type EntropyNewEvent struct {
	Source     string    `json:"source"`
	Base64Data string    `json:"base64Data"`
	ByteCount  int       `json:"byteCount"`
	Timestamp  time.Time `json:"timestamp"`
}

// EntropyValidatedEvent is published when entropy passes validation and is saved.
type EntropyValidatedEvent struct {
	ID        uint      `json:"id"`
	Source    string    `json:"source"`
	ByteCount int       `json:"byteCount"`
	PoolSize  int64     `json:"poolSize"`
	Timestamp time.Time `json:"timestamp"`
}

// KeyCreatedEvent is published when a new RSA key is generated.
type KeyCreatedEvent struct {
	ID        uint      `json:"id"`
	Alias     string    `json:"alias"`
	KeySize   int       `json:"keySize"`
	Timestamp time.Time `json:"timestamp"`
}

// KeyExportedEvent is published when a key is exported via Key Wrapping.
type KeyExportedEvent struct {
	ID        uint      `json:"id"`
	Alias     string    `json:"alias"`
	Algorithm string    `json:"algorithm"`
	Timestamp time.Time `json:"timestamp"`
}

// KeyDeletedEvent is published when a key is deleted.
type KeyDeletedEvent struct {
	ID        uint      `json:"id"`
	Alias     string    `json:"alias"`
	Timestamp time.Time `json:"timestamp"`
}

// AuditStartEvent is published to trigger an entropy audit.
type AuditStartEvent struct {
	RequestedSize int       `json:"requestedSize"`
	Timestamp     time.Time `json:"timestamp"`
}

// AuditCompleteEvent is published when an audit completes.
type AuditCompleteEvent struct {
	SampleSize int         `json:"sampleSize"`
	Results    interface{} `json:"results"`
	Timestamp  time.Time   `json:"timestamp"`
}
