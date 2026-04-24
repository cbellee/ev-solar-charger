package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// SecretStore persists Tesla tokens to a backing secret store.
type SecretStore interface {
	SetSecret(ctx context.Context, name, value string) error
}

// KeyVaultSecretStore stores secrets in Azure Key Vault.
type KeyVaultSecretStore struct {
	client *azsecrets.Client
}

// NewKeyVaultSecretStore creates a Key Vault-backed secret writer using managed identity.
func NewKeyVaultSecretStore(vaultURI string) (*KeyVaultSecretStore, error) {
	if strings.TrimSpace(vaultURI) == "" {
		return nil, fmt.Errorf("KEY_VAULT_URI must not be empty")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create default Azure credential: %w", err)
	}

	client, err := azsecrets.NewClient(vaultURI, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create Key Vault secrets client: %w", err)
	}

	return &KeyVaultSecretStore{client: client}, nil
}

// SetSecret creates or updates a Key Vault secret.
func (s *KeyVaultSecretStore) SetSecret(ctx context.Context, name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("secret name must not be empty")
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("secret value must not be empty")
	}

	_, err := s.client.SetSecret(ctx, name, azsecrets.SetSecretParameters{Value: stringPtr(value)}, nil)
	if err != nil {
		return fmt.Errorf("set Key Vault secret %q: %w", name, err)
	}
	return nil
}

func stringPtr(value string) *string {
	return &value
}
