package keymanager

import "gorm.io/gorm"

// QuantumData stores raw quantum entropy fetched from the LfD API.
type QuantumData struct {
	gorm.Model
	DataBase64 string `gorm:"not null"`
	Used       bool   `gorm:"default:false;index"`
	Source     string `gorm:"not null;index"`
}

// RsaKey stores a generated RSA key pair. The private key is AES-256-GCM wrapped.
type RsaKey struct {
	gorm.Model
	Alias               string `gorm:"uniqueIndex;not null"`
	KeySize             int    `gorm:"not null"`
	PublicKeyPEM        string `gorm:"not null"`
	EncryptedPrivatePEM []byte `gorm:"not null"`
	Nonce               []byte `gorm:"not null"` // AES-GCM nonce
}
