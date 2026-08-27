package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const keySize = 32

type Envelope struct {
	EncryptedDataKey []byte `json:"encrypted_data_key"`
	KeyNonce         []byte `json:"key_nonce"`
	DataNonce        []byte `json:"data_nonce"`
	Ciphertext       []byte `json:"ciphertext"`
}

type Cipher struct{ master cipher.AEAD }

func NewCipher(masterKey []byte) (*Cipher, error) {
	if len(masterKey) != keySize {
		return nil, fmt.Errorf("master key must contain exactly %d bytes", keySize)
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("create master cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create master AEAD: %w", err)
	}
	return &Cipher{master: aead}, nil
}

func ParseMasterKey(encoded string) ([]byte, error) {
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("master key must be standard base64")
	}
	if len(value) != keySize {
		return nil, fmt.Errorf("decoded master key must contain exactly %d bytes", keySize)
	}
	return value, nil
}

func GenerateMasterKey() (string, error) {
	value := make([]byte, keySize)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate master key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(value), nil
}

func (c *Cipher) Seal(plaintext, associatedData []byte) (Envelope, error) {
	dataKey := make([]byte, keySize)
	if _, err := rand.Read(dataKey); err != nil {
		return Envelope{}, fmt.Errorf("generate data key: %w", err)
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return Envelope{}, err
	}
	dataCipher, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, err
	}
	dataNonce := make([]byte, dataCipher.NonceSize())
	keyNonce := make([]byte, c.master.NonceSize())
	if _, err := rand.Read(dataNonce); err != nil {
		return Envelope{}, err
	}
	if _, err := rand.Read(keyNonce); err != nil {
		return Envelope{}, err
	}
	return Envelope{
		EncryptedDataKey: c.master.Seal(nil, keyNonce, dataKey, append([]byte("yuanci:dek:"), associatedData...)),
		KeyNonce:         keyNonce, DataNonce: dataNonce,
		Ciphertext: dataCipher.Seal(nil, dataNonce, plaintext, associatedData),
	}, nil
}

func (c *Cipher) Open(envelope Envelope, associatedData []byte) ([]byte, error) {
	dataKey, err := c.master.Open(nil, envelope.KeyNonce, envelope.EncryptedDataKey, append([]byte("yuanci:dek:"), associatedData...))
	if err != nil {
		return nil, errors.New("decrypt data key: authentication failed")
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, errors.New("decrypt secret: invalid data key")
	}
	dataCipher, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("decrypt secret: invalid cipher")
	}
	plaintext, err := dataCipher.Open(nil, envelope.DataNonce, envelope.Ciphertext, associatedData)
	if err != nil {
		return nil, errors.New("decrypt secret: authentication failed")
	}
	return plaintext, nil
}
