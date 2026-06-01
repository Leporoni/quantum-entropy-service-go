package keymanager

import (
	"errors"

	"gorm.io/gorm"
)

// Repository handles all database operations for QuantumData and RsaKey.
type Repository struct {
	db *gorm.DB
}

// NewRepository creates a new Repository and auto-migrates the schema.
func NewRepository(db *gorm.DB) (*Repository, error) {
	if err := db.AutoMigrate(&QuantumData{}, &RsaKey{}); err != nil {
		return nil, err
	}
	return &Repository{db: db}, nil
}

// --- QuantumData ---

// SaveEntropy persists a new QuantumData record.
func (r *Repository) SaveEntropy(q *QuantumData) error {
	return r.db.Create(q).Error
}

// CountAllUnusedEntropy returns the count of unused entropy records.
func (r *Repository) CountAllUnusedEntropy() (int64, error) {
	var count int64
	err := r.db.Model(&QuantumData{}).Where("used = ?", false).Count(&count).Error
	return count, err
}

// FindAllUnusedBySource returns all unused entropy records for a given source.
func (r *Repository) FindAllUnusedBySource(source string) ([]QuantumData, error) {
	var records []QuantumData
	err := r.db.Where("used = ? AND source = ?", false, source).Find(&records).Error
	return records, err
}

// ConsumeEntropy fetches n unused records and marks them as used atomically.
func (r *Repository) ConsumeEntropy(n int) ([]QuantumData, error) {
	var records []QuantumData
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("used = ?", false).Limit(n).Find(&records).Error; err != nil {
			return err
		}
		if len(records) < n {
			return errors.New("insufficient entropy in pool")
		}
		ids := make([]uint, len(records))
		for i, rec := range records {
			ids[i] = rec.ID
		}
		return tx.Model(&QuantumData{}).Where("id IN ?", ids).Update("used", true).Error
	})
	return records, err
}

// --- RsaKey ---

// SaveKey persists a new RsaKey record.
func (r *Repository) SaveKey(k *RsaKey) error {
	return r.db.Create(k).Error
}

// FindAllKeys returns all RSA keys.
func (r *Repository) FindAllKeys() ([]RsaKey, error) {
	var keys []RsaKey
	err := r.db.Find(&keys).Error
	return keys, err
}

// FindKeyByID returns a single RSA key by ID.
func (r *Repository) FindKeyByID(id uint) (*RsaKey, error) {
	var key RsaKey
	err := r.db.First(&key, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &key, err
}

// DeleteKeyByID hard-deletes an RSA key by ID.
func (r *Repository) DeleteKeyByID(id uint) error {
	return r.db.Unscoped().Delete(&RsaKey{}, id).Error
}

// DeleteAllKeys hard-deletes all RSA keys.
func (r *Repository) DeleteAllKeys() error {
	return r.db.Unscoped().Where("1 = 1").Delete(&RsaKey{}).Error
}
