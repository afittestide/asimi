package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/afittestide/asimi/internal/keyring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain sets up test environment
func TestMain(m *testing.M) {
	// Use a test-specific keyring service to avoid polluting production credentials
	original := os.Getenv("ASIMI_KEYRING_SERVICE")
	os.Setenv("ASIMI_KEYRING_SERVICE", "dev.asimi.asimi-cli-test")

	// Unless explicitly testing the real keyring, use the in-memory mock
	// to avoid macOS Keychain Access prompts during tests.
	if os.Getenv("ASIMI_TEST_KEYRING") != "1" {
		keyring.MockKeyring()
	}

	// Keep tests away from the developer's real gcloud project. Otherwise a
	// configured host makes Vertex AI look authenticated in tests that expect
	// no credentials.
	gcloudDir := ""
	if dir, err := os.MkdirTemp("", "asimi-test-gcloud"); err == nil {
		gcloudDir = dir
		os.Setenv("CLOUDSDK_CONFIG", dir)
	}

	// Run tests
	code := m.Run()

	if gcloudDir != "" {
		os.RemoveAll(gcloudDir)
	}
	os.Setenv("ASIMI_KEYRING_SERVICE", original)

	// Exit with test result code
	os.Exit(code)
}

// TestKeyringErrorHandling tests error handling without touching the keyring
func TestKeyringErrorHandling(t *testing.T) {
	t.Run("delete non-existent API key should not error", func(t *testing.T) {
		// This test documents expected behavior
		t.Log("DeleteAPIKeyFromKeyring should not error for non-existent keys")
	})
}

// NOTE: The following tests require the system keyring and will trigger system dialogs.
// They are DISABLED by default and only run when explicitly requested with:
//   ASIMI_TEST_KEYRING=1 go test -v -run TestKeyring
//
// These tests are kept for manual verification but should NOT run in CI/CD or regular test runs.

func TestKeyringIntegration(t *testing.T) {
	// Skip unless explicitly enabled
	if os.Getenv("ASIMI_TEST_KEYRING") != "1" {
		t.Skip("Skipping keyring integration tests. Set ASIMI_TEST_KEYRING=1 to run these tests manually.")
	}

	t.Log("⚠️  WARNING: These tests will trigger system keyring dialogs!")
	t.Log("⚠️  Make sure you're ready to interact with keyring prompts.")

	provider := "asimi-test-" + "apikey"
	apiKey := "sk-test-api-key-12345"

	// Clean up after test
	defer DeleteAPIKeyFromKeyring(provider)

	t.Run("API key storage lifecycle", func(t *testing.T) {
		// Save API key
		err := SaveAPIKeyToKeyring(provider, apiKey)
		if err != nil {
			t.Fatalf("Failed to save API key: %v", err)
		}

		// Retrieve API key
		retrievedKey, err := GetAPIKeyFromKeyring(provider)
		assert.NoError(t, err)
		assert.Equal(t, apiKey, retrievedKey)

		// Delete API key
		err = DeleteAPIKeyFromKeyring(provider)
		assert.NoError(t, err)

		// Verify it's gone
		key, err := GetAPIKeyFromKeyring(provider)
		assert.NoError(t, err)
		assert.Equal(t, "", key)
	})
}

// NOTE: UpdateUserLLMAuth tests are disabled because they trigger system keyring dialogs.
// To test this function manually, set ASIMI_TEST_KEYRING=1 and run:
//
//	ASIMI_TEST_KEYRING=1 go test -v -run TestUpdateUserLLMAuthIntegration
func TestUpdateUserLLMAuthIntegration(t *testing.T) {
	// Skip unless explicitly enabled
	if os.Getenv("ASIMI_TEST_KEYRING") != "1" {
		t.Skip("Skipping UpdateUserLLMAuth test. Set ASIMI_TEST_KEYRING=1 to run this test manually.")
	}

	t.Log("WARNING: This test will trigger system keyring dialogs!")

	t.Run("creates config file if not exists", func(t *testing.T) {
		tempHome := t.TempDir()
		originalHome := os.Getenv("HOME")
		os.Setenv("HOME", tempHome)
		defer os.Setenv("HOME", originalHome)

		err := UpdateUserLLMAuth("openai", "test-api-key", "gpt-4")
		require.NoError(t, err)

		configDir := filepath.Join(tempHome, ".config", "asimi")
		_, err = os.Stat(configDir)
		require.NoError(t, err)

		configPath := filepath.Join(configDir, "asimi.conf")
		_, err = os.Stat(configPath)
		assert.NoError(t, err, "Config file should be created")
	})
}
