package store

import (
	"testing"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestValidBackupTaskResultRequiresIntegrityMetadata(t *testing.T) {
	checkpoint := `{"backupId":"backup-1","storageObjectKey":"backups/backup-1.tar.gz"}`
	valid := `{"backupId":"backup-1","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":0,"storageLocation":"backups/backup-1.tar.gz"}`
	if !validBackupTaskResult(checkpoint, []byte(valid)) {
		t.Fatal("valid zero-byte backup result was rejected")
	}

	tests := []struct {
		name   string
		result string
	}{
		{name: "missing backup id", result: `{"checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "wrong backup id", result: `{"backupId":"backup-2","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "missing checksum", result: `{"backupId":"backup-1","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "missing manifest digest", result: `{"backupId":"backup-1","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sizeBytes":12,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "negative size", result: `{"backupId":"backup-1","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":-1,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "path escape", result: `{"backupId":"backup-1","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"../backup.tar.gz"}`},
		{name: "wrong digest length", result: `{"backupId":"backup-1","checksum":"sha256:aa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"backups/backup-1.tar.gz"}`},
		{name: "storage key mismatch", result: `{"backupId":"backup-1","checksum":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sizeBytes":12,"storageLocation":"backups/other.tar.gz"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validBackupTaskResult(checkpoint, []byte(test.result)) {
				t.Fatal("invalid backup result was accepted")
			}
		})
	}
}

func TestProvisionPayloadNeverIncludesSecretPlaintext(t *testing.T) {
	definitions := []domain.StartupVariable{
		{Key: "memory_mb", Secret: false},
		{Key: "rcon_password", Secret: true},
	}
	values := map[string]any{"memory_mb": int64(2048), "rcon_password": "do-not-persist"}
	variables := stringifiedNonSecretStartupValues(definitions, values)
	if variables["rcon_password"] != "" {
		t.Fatalf("secret value leaked into provision variables: %#v", variables)
	}
	if variables["memory_mb"] != "2048" {
		t.Fatalf("non-secret value missing: %#v", variables)
	}
	keys := secretStartupKeys(definitions)
	if len(keys) != 1 || keys[0] != "rcon_password" {
		t.Fatalf("secret keys = %#v", keys)
	}
}
