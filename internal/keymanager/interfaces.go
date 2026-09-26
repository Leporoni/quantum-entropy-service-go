package keymanager

// EntropyStore abstracts persistence of quantum entropy records.
// *Repository satisfies it implicitly; consumers depend on the
// abstraction so they can be tested with mocks.
type EntropyStore interface {
	SaveEntropy(q *QuantumData) error
	ConsumeEntropy(n int) ([]QuantumData, error)
	CountAllUnusedEntropy() (int64, error)
	FindAllUnusedBySource(source string) ([]QuantumData, error)
}

// KeyStore abstracts persistence of RSA keys.
type KeyStore interface {
	SaveKey(k *RsaKey) error
	FindAllKeys() ([]RsaKey, error)
	FindKeyByID(id uint) (*RsaKey, error)
	DeleteKeyByID(id uint) error
	DeleteAllKeys() error
}

// Compile-time assertions: *Repository satisfies both interfaces.
var (
	_ EntropyStore = (*Repository)(nil)
	_ KeyStore     = (*Repository)(nil)
)