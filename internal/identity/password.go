package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const argon2Version = 19

// Argon2id uses substantial memory per derivation. Keep the process-wide
// number of concurrent derivations bounded so a burst spread across multiple
// source IPs cannot exhaust the control plane's memory before request-level
// rate limits have a chance to react.
const argon2ConcurrencyLimit = 2

var argon2IDKey = argon2.IDKey
var argon2Gate = make(chan struct{}, argon2ConcurrencyLimit)

var ErrInvalidPasswordHash = errors.New("invalid Argon2id password hash")

type Argon2idParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultArgon2idParams() Argon2idParams {
	return Argon2idParams{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

func HashPassword(password string, params Argon2idParams) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	if err := validateParams(params); err != nil {
		return "", err
	}
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	digest := deriveArgon2IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2Version,
		params.MemoryKiB,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	), nil
}

func VerifyPassword(encoded string, password string) (bool, error) {
	params, salt, expected, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	actual := deriveArgon2IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func deriveArgon2IDKey(password []byte, salt []byte, iterations uint32, memoryKiB uint32, parallelism uint8, keyLength uint32) []byte {
	argon2Gate <- struct{}{}
	defer func() { <-argon2Gate }()
	return argon2IDKey(password, salt, iterations, memoryKiB, parallelism, keyLength)
}

func parsePHC(encoded string) (Argon2idParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	parameterParts := strings.Split(parts[3], ",")
	if len(parameterParts) != 3 {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	memory, errMemory := parseUintParameter(parameterParts[0], "m", 32)
	iterations, errIterations := parseUintParameter(parameterParts[1], "t", 32)
	parallelism, errParallelism := parseUintParameter(parameterParts[2], "p", 8)
	if errMemory != nil || errIterations != nil || errParallelism != nil {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params := Argon2idParams{
		MemoryKiB:   uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(digest)),
	}
	if err := validateParams(params); err != nil {
		return Argon2idParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, digest, nil
}

func parseUintParameter(value string, name string, bitSize int) (uint64, error) {
	prefix := name + "="
	if !strings.HasPrefix(value, prefix) {
		return 0, ErrInvalidPasswordHash
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, bitSize)
	if err != nil {
		return 0, ErrInvalidPasswordHash
	}
	return parsed, nil
}

func validateParams(params Argon2idParams) error {
	if params.MemoryKiB < 8*1024 || params.MemoryKiB > 1024*1024 {
		return errors.New("Argon2id memory must be between 8 MiB and 1 GiB")
	}
	if params.Iterations < 1 || params.Iterations > 20 {
		return errors.New("Argon2id iterations must be between 1 and 20")
	}
	if params.Parallelism < 1 || params.Parallelism > 16 {
		return errors.New("Argon2id parallelism must be between 1 and 16")
	}
	if params.SaltLength < 16 || params.SaltLength > 64 {
		return errors.New("Argon2id salt must be between 16 and 64 bytes")
	}
	if params.KeyLength < 16 || params.KeyLength > 64 {
		return errors.New("Argon2id key must be between 16 and 64 bytes")
	}
	return nil
}
