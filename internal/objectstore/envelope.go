package objectstore

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	envelopeVersion = "gugu-envelope/v1"
	chunkSize       = 4 * 1024 * 1024
)

var envelopeMagic = [8]byte{'G', 'U', 'G', 'U', 'E', 'N', 'C', '1'}

type Keyring struct {
	activeID string
	keys     map[string][32]byte
}

func NewKeyring(activeID string, materials map[string][]byte) (*Keyring, error) {
	activeID = strings.TrimSpace(activeID)
	if activeID == "" || len(materials) == 0 {
		return nil, errors.New("object keyring requires an active key and key material")
	}
	keyring := &Keyring{activeID: activeID, keys: make(map[string][32]byte, len(materials))}
	for keyID, material := range materials {
		if strings.TrimSpace(keyID) == "" || len(material) == 0 || strings.ContainsAny(keyID, ":\r\n") {
			return nil, errors.New("object keyring contains an invalid entry")
		}
		keyring.keys[keyID] = sha256.Sum256(material)
	}
	if _, ok := keyring.keys[activeID]; !ok {
		return nil, fmt.Errorf("active object key %q is unavailable", activeID)
	}
	return keyring, nil
}

// Manifest is stored in PostgreSQL and signed independently from the remote
// object. WrappedDataKey contains only an encrypted random data key; the
// object-store provider never receives the platform master key.
type Manifest struct {
	Version          string `json:"version"`
	ObjectKey        string `json:"objectKey"`
	PlaintextDigest  string `json:"plaintextDigest"`
	CiphertextDigest string `json:"ciphertextDigest"`
	PlaintextSize    int64  `json:"plaintextSize"`
	CiphertextSize   int64  `json:"ciphertextSize"`
	KeyID            string `json:"keyId"`
	WrappedDataKey   string `json:"wrappedDataKey"`
	ChunkSize        int    `json:"chunkSize"`
	MAC              string `json:"mac"`
}

type streamResult struct {
	plainDigest, cipherDigest string
	plainSize, cipherSize     int64
	err                       error
}

func EncryptUpload(ctx context.Context, store Store, key string, source io.Reader, plaintextSize int64, keyring *Keyring) (Manifest, error) {
	if keyring == nil {
		return Manifest{}, errors.New("object keyring is required")
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return Manifest{}, fmt.Errorf("generate object data key: %w", err)
	}
	wrapped, err := keyring.wrap(dataKey, key)
	if err != nil {
		return Manifest{}, err
	}
	reader, writer := io.Pipe()
	resultChannel := make(chan streamResult, 1)
	go func() {
		result := encryptStream(ctx, writer, source, dataKey, key)
		if result.err != nil {
			_ = writer.CloseWithError(result.err)
		} else {
			_ = writer.Close()
		}
		resultChannel <- result
	}()
	_, putErr := store.Put(ctx, key, reader, -1, PutOptions{ContentType: "application/vnd.gugumanager.encrypted-backup", Metadata: map[string]string{
		"gugu-envelope": envelopeVersion, "gugu-key-id": keyring.activeID,
	}})
	if putErr != nil {
		_ = reader.CloseWithError(putErr)
	}
	result := <-resultChannel
	if putErr != nil {
		return Manifest{}, fmt.Errorf("upload encrypted object: %w", putErr)
	}
	if result.err != nil {
		return Manifest{}, result.err
	}
	if plaintextSize >= 0 && plaintextSize != result.plainSize {
		_ = store.Delete(context.Background(), key)
		return Manifest{}, fmt.Errorf("plaintext size mismatch: read %d, expected %d", result.plainSize, plaintextSize)
	}
	manifest := Manifest{
		Version: envelopeVersion, ObjectKey: key, PlaintextDigest: result.plainDigest,
		CiphertextDigest: result.cipherDigest, PlaintextSize: result.plainSize, CiphertextSize: result.cipherSize,
		KeyID: keyring.activeID, WrappedDataKey: wrapped, ChunkSize: chunkSize,
	}
	if err := keyring.sign(&manifest); err != nil {
		_ = store.Delete(context.Background(), key)
		return Manifest{}, err
	}
	return manifest, nil
}

func DecryptDownload(ctx context.Context, store Store, manifest Manifest, destination io.Writer, keyring *Keyring) error {
	if keyring == nil {
		return errors.New("object keyring is required")
	}
	if err := keyring.verify(manifest); err != nil {
		return err
	}
	dataKey, err := keyring.unwrap(manifest.KeyID, manifest.WrappedDataKey, manifest.ObjectKey)
	if err != nil {
		return err
	}
	reader, info, err := store.Get(ctx, manifest.ObjectKey)
	if err != nil {
		return err
	}
	defer reader.Close()
	if info.Size != manifest.CiphertextSize {
		return errors.New("encrypted object size does not match signed manifest")
	}
	plainHash, cipherHash := sha256.New(), sha256.New()
	counting := &countingReader{reader: io.TeeReader(reader, cipherHash)}
	plainSize, err := decryptStream(ctx, counting, io.MultiWriter(destination, plainHash), dataKey, manifest.ObjectKey)
	if err != nil {
		return err
	}
	if counting.count != manifest.CiphertextSize || plainSize != manifest.PlaintextSize ||
		"sha256:"+hex.EncodeToString(plainHash.Sum(nil)) != manifest.PlaintextDigest ||
		"sha256:"+hex.EncodeToString(cipherHash.Sum(nil)) != manifest.CiphertextDigest {
		return errors.New("backup object integrity verification failed")
	}
	return nil
}

func encryptStream(ctx context.Context, destination io.Writer, source io.Reader, key []byte, objectKey string) streamResult {
	block, err := aes.NewCipher(key)
	if err != nil {
		return streamResult{err: err}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return streamResult{err: err}
	}
	noncePrefix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, noncePrefix); err != nil {
		return streamResult{err: err}
	}
	plainHash, cipherHash := sha256.New(), sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(destination, cipherHash)}
	header := make([]byte, 20)
	copy(header, envelopeMagic[:])
	binary.BigEndian.PutUint32(header[8:12], chunkSize)
	copy(header[12:], noncePrefix)
	if _, err := counter.Write(header); err != nil {
		return streamResult{err: err}
	}
	buffer := make([]byte, chunkSize)
	var plainSize int64
	for sequence := uint32(0); ; sequence++ {
		if err := ctx.Err(); err != nil {
			return streamResult{err: err}
		}
		read, readErr := io.ReadFull(source, buffer)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return streamResult{err: readErr}
		}
		if read == 0 {
			if err := binary.Write(counter, binary.BigEndian, uint32(0)); err != nil {
				return streamResult{err: err}
			}
			break
		}
		plainHash.Write(buffer[:read])
		plainSize += int64(read)
		nonce := makeNonce(noncePrefix, sequence)
		aad := chunkAAD(objectKey, sequence, uint32(read))
		sealed := aead.Seal(nil, nonce, buffer[:read], aad)
		if err := binary.Write(counter, binary.BigEndian, uint32(read)); err != nil {
			return streamResult{err: err}
		}
		if _, err := counter.Write(sealed); err != nil {
			return streamResult{err: err}
		}
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			if err := binary.Write(counter, binary.BigEndian, uint32(0)); err != nil {
				return streamResult{err: err}
			}
			break
		}
	}
	return streamResult{
		plainDigest: "sha256:" + hex.EncodeToString(plainHash.Sum(nil)), cipherDigest: "sha256:" + hex.EncodeToString(cipherHash.Sum(nil)),
		plainSize: plainSize, cipherSize: counter.count,
	}
}

func decryptStream(ctx context.Context, source io.Reader, destination io.Writer, key []byte, objectKey string) (int64, error) {
	header := make([]byte, 20)
	if _, err := io.ReadFull(source, header); err != nil {
		return 0, err
	}
	if !bytes.Equal(header[:8], envelopeMagic[:]) || binary.BigEndian.Uint32(header[8:12]) != chunkSize {
		return 0, errors.New("unsupported encrypted backup header")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return 0, err
	}
	noncePrefix := header[12:20]
	var total int64
	for sequence := uint32(0); ; sequence++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var length uint32
		if err := binary.Read(source, binary.BigEndian, &length); err != nil {
			return total, err
		}
		if length == 0 {
			break
		}
		if length > chunkSize {
			return total, errors.New("encrypted backup chunk exceeds limit")
		}
		sealed := make([]byte, int(length)+aead.Overhead())
		if _, err := io.ReadFull(source, sealed); err != nil {
			return total, err
		}
		plaintext, err := aead.Open(nil, makeNonce(noncePrefix, sequence), sealed, chunkAAD(objectKey, sequence, length))
		if err != nil {
			return total, errors.New("encrypted backup authentication failed")
		}
		if _, err := destination.Write(plaintext); err != nil {
			return total, err
		}
		total += int64(len(plaintext))
	}
	var trailing [1]byte
	if count, err := source.Read(trailing[:]); count != 0 || (err != nil && err != io.EOF) {
		return total, errors.New("encrypted backup contains trailing data")
	}
	return total, nil
}

func makeNonce(prefix []byte, sequence uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], sequence)
	return nonce
}

func chunkAAD(objectKey string, sequence, length uint32) []byte {
	result := make([]byte, len(objectKey)+8)
	copy(result, objectKey)
	binary.BigEndian.PutUint32(result[len(objectKey):], sequence)
	binary.BigEndian.PutUint32(result[len(objectKey)+4:], length)
	return result
}

func (k *Keyring) wrap(dataKey []byte, objectKey string) (string, error) {
	aead, err := keyAEAD(k.keys[k.activeID])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, dataKey, []byte(objectKey))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (k *Keyring) unwrap(keyID, encoded, objectKey string) ([]byte, error) {
	key, ok := k.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("object wrapping key %q is unavailable", keyID)
	}
	aead, err := keyAEAD(key)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("wrapped object key is malformed")
	}
	return aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(objectKey))
}

func keyAEAD(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (k *Keyring) sign(manifest *Manifest) error {
	manifest.MAC = ""
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	key := k.keys[manifest.KeyID]
	mac := hmac.New(sha256.New, key[:])
	mac.Write(canonical)
	manifest.MAC = base64.RawStdEncoding.EncodeToString(mac.Sum(nil))
	return nil
}

func (k *Keyring) verify(manifest Manifest) error {
	if manifest.Version != envelopeVersion || manifest.ObjectKey == "" || manifest.ChunkSize != chunkSize ||
		manifest.PlaintextSize < 0 || manifest.CiphertextSize <= 0 {
		return errors.New("object manifest is incomplete")
	}
	key, ok := k.keys[manifest.KeyID]
	if !ok {
		return fmt.Errorf("object manifest key %q is unavailable", manifest.KeyID)
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.MAC)
	if err != nil {
		return errors.New("object manifest MAC is malformed")
	}
	manifest.MAC = ""
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(canonical)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("object manifest signature verification failed")
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(value []byte) (int, error) {
	written, err := w.writer.Write(value)
	w.count += int64(written)
	return written, err
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(value []byte) (int, error) {
	read, err := r.reader.Read(value)
	r.count += int64(read)
	return read, err
}
