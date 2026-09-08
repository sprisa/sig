package config

import (
	"errors"

	"github.com/zalando/go-keyring"
)

var ErrCredentialNotFound = errors.New("credential not found")

type Credentials interface {
	Get(id string) (string, error)
	Set(id, key string) error
	Delete(id string) error
}

type Keyring struct{}

func (Keyring) Get(id string) (string, error) {
	key, err := keyring.Get("sig", id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrCredentialNotFound
	}
	return key, err
}

func (Keyring) Set(id, key string) error { return keyring.Set("sig", id, key) }

func (Keyring) Delete(id string) error {
	err := keyring.Delete("sig", id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
