package agent

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var (
	errBackupIntegrity = errors.New("backup integrity validation failed")
	errBackupPath      = errors.New("invalid backup storage path")
	backupIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)
)

const restoreMarkerVersion = 1

type backupMetadata struct {
	Checksum       string
	ManifestDigest string
	SizeBytes      int64
}

type restoreMarker struct {
	Version  int    `json:"version"`
	ServerID string `json:"serverId"`
	Staging  string `json:"staging"`
	Previous string `json:"previous"`
	Phase    string `json:"phase"`
}

func validateBackupID(backupID string) error {
	if !backupIDPattern.MatchString(backupID) {
		return fmt.Errorf("%w: invalid backup id", errBackupPath)
	}
	return nil
}

func canonicalBackupObjectKey(backupID string) (string, error) {
	if err := validateBackupID(backupID); err != nil {
		return "", err
	}
	return "backups/" + backupID + ".tar.gz", nil
}

func ensureBackupDirectory(dataRoot string) (string, error) {
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	backupDir := filepath.Join(root, "backups")
	if info, err := os.Lstat(backupDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: backup directory is not a real directory", errBackupPath)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	return backupDir, nil
}

func resolveBackupArchive(dataRoot, backupID, storageObjectKey string) (string, error) {
	expected, err := canonicalBackupObjectKey(backupID)
	if err != nil {
		return "", err
	}
	if storageObjectKey != "" && path.Clean(strings.ReplaceAll(storageObjectKey, `\`, "/")) != expected {
		return "", fmt.Errorf("%w: storage object key does not match backup id", errBackupPath)
	}
	backupDir, err := ensureBackupDirectory(dataRoot)
	if err != nil {
		return "", err
	}
	archive := filepath.Join(backupDir, backupID+".tar.gz")
	relative, err := filepath.Rel(backupDir, archive)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: archive escapes backup directory", errBackupPath)
	}
	return archive, nil
}

func fileChecksum(filePath string) (string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func inspectBackupArchive(archivePath string) (backupMetadata, error) {
	sum, size, err := fileChecksum(archivePath)
	if err != nil {
		return backupMetadata{}, err
	}
	manifestDigest, err := backupManifestDigest(archivePath)
	if err != nil {
		return backupMetadata{}, err
	}
	return backupMetadata{
		Checksum:       "sha256:" + sum,
		ManifestDigest: manifestDigest,
		SizeBytes:      size,
	}, nil
}

// extractDockerBackupPayload extracts the gzip file wrapped by Docker's copy
// API into a host-side temporary file. The returned file is a real .tar.gz,
// not Docker's outer tar stream.
func extractDockerBackupPayload(dockerArchivePath, backupDir, expectedName, backupID string) (resultPath string, resultErr error) {
	outerFile, err := os.Open(dockerArchivePath)
	if err != nil {
		return "", err
	}
	defer outerFile.Close()
	outer := tar.NewReader(outerFile)
	for {
		header, err := outer.Next()
		if err == io.EOF {
			return "", fmt.Errorf("backup payload %q missing from docker archive", expectedName)
		}
		if err != nil {
			return "", fmt.Errorf("read docker backup archive: %w", err)
		}
		clean := path.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || path.Base(clean) != expectedName {
			continue
		}
		if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
			return "", fmt.Errorf("%w: unsafe docker archive path", errBackupIntegrity)
		}
		output, err := os.CreateTemp(backupDir, "."+backupID+"-*.tar.gz.partial")
		if err != nil {
			return "", err
		}
		outputPath := output.Name()
		keep := false
		defer func() {
			_ = output.Close()
			if !keep {
				_ = os.Remove(outputPath)
			}
		}()
		written, copyErr := io.Copy(output, outer)
		if copyErr == nil && written != header.Size {
			copyErr = io.ErrUnexpectedEOF
		}
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeErr := output.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract docker backup payload: %w", copyErr)
		}
		if closeErr != nil {
			return "", closeErr
		}
		keep = true
		return outputPath, nil
	}
}

// publishBackupArchive makes a validated partial archive visible without ever
// replacing an existing immutable backup. A hard link gives us an atomic
// create-if-absent operation; the exclusive-copy fallback supports filesystems
// that do not allow hard links.
func publishBackupArchive(partialPath, finalPath string) error {
	if err := os.Link(partialPath, finalPath); err == nil {
		_ = os.Remove(partialPath)
		return syncDirectory(filepath.Dir(finalPath))
	} else if os.IsExist(err) {
		return err
	}
	source, err := os.Open(partialPath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = destination.Close()
		if !keep {
			_ = os.Remove(finalPath)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	keep = true
	_ = os.Remove(partialPath)
	return syncDirectory(filepath.Dir(finalPath))
}

func backupManifestDigest(archivePath string) (string, error) {
	payload, err := openBackupPayload(archivePath)
	if err != nil {
		return "", err
	}
	defer payload.Close()
	return canonicalTarManifestDigest(payload.reader)
}

type backupPayloadReader struct {
	file       *os.File
	compressed *gzip.Reader
	reader     *tar.Reader
}

func openBackupPayload(archivePath string) (*backupPayloadReader, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	compressed, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("open backup payload: %w", err)
	}
	return &backupPayloadReader{file: file, compressed: compressed, reader: tar.NewReader(compressed)}, nil
}

func (p *backupPayloadReader) Close() error {
	compressedErr := p.compressed.Close()
	fileErr := p.file.Close()
	if compressedErr != nil {
		return compressedErr
	}
	return fileErr
}

func canonicalTarManifestDigest(reader *tar.Reader) (string, error) {
	hash := sha256.New()
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read backup manifest: %w", err)
		}
		_, skip, err := writeManifestHeader(hash, header)
		if err != nil {
			return "", err
		}
		if skip {
			continue
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			contentHash := sha256.New()
			if _, err := io.Copy(contentHash, reader); err != nil {
				return "", fmt.Errorf("hash backup content: %w", err)
			}
			if _, err := io.WriteString(hash, hex.EncodeToString(contentHash.Sum(nil))); err != nil {
				return "", err
			}
		}
		if _, err := hash.Write([]byte{0}); err != nil {
			return "", err
		}
		entries++
	}
	if entries == 0 {
		return "", fmt.Errorf("backup manifest is empty")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func writeManifestHeader(writer io.Writer, header *tar.Header) (string, bool, error) {
	clean := path.Clean(header.Name)
	if clean == "." && header.Typeflag == tar.TypeDir {
		return clean, true, nil
	}
	if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("unsafe backup manifest path %q", header.Name)
	}
	if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return "", false, fmt.Errorf("unsupported backup entry type %d", header.Typeflag)
	}
	if _, err := io.WriteString(writer, clean); err != nil {
		return "", false, err
	}
	if _, err := writer.Write([]byte{0, header.Typeflag, 0}); err != nil {
		return "", false, err
	}
	if _, err := io.WriteString(writer, strconv.FormatInt(header.Size, 10)); err != nil {
		return "", false, err
	}
	if _, err := writer.Write([]byte{0}); err != nil {
		return "", false, err
	}
	if _, err := io.WriteString(writer, strconv.FormatInt(header.Mode&0o777, 8)); err != nil {
		return "", false, err
	}
	if _, err := writer.Write([]byte{0}); err != nil {
		return "", false, err
	}
	return clean, false, nil
}

func extractBackupArchive(archivePath, destination string) (string, error) {
	payload, err := openBackupPayload(archivePath)
	if err != nil {
		return "", err
	}
	defer payload.Close()
	hash := sha256.New()
	entries := 0
	for {
		header, err := payload.reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read backup payload: %w", err)
		}
		clean, skip, err := writeManifestHeader(hash, header)
		if err != nil {
			return "", err
		}
		if skip {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("backup entry escapes staging directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, backupDirectoryMode(header.Mode)); err != nil {
				return "", fmt.Errorf("create backup directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", fmt.Errorf("create backup parent: %w", err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFileMode(header.Mode))
			if err != nil {
				return "", fmt.Errorf("create backup file: %w", err)
			}
			contentHash := sha256.New()
			_, copyErr := io.Copy(io.MultiWriter(file, contentHash), payload.reader)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil {
				return "", fmt.Errorf("extract backup file: %w", copyErr)
			}
			if syncErr != nil {
				return "", fmt.Errorf("sync backup file: %w", syncErr)
			}
			if closeErr != nil {
				return "", fmt.Errorf("close backup file: %w", closeErr)
			}
			if _, err := io.WriteString(hash, hex.EncodeToString(contentHash.Sum(nil))); err != nil {
				return "", err
			}
		}
		if _, err := hash.Write([]byte{0}); err != nil {
			return "", err
		}
		entries++
	}
	if entries == 0 {
		return "", fmt.Errorf("backup manifest is empty")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func backupDirectoryMode(mode int64) os.FileMode {
	permissions := os.FileMode(mode) & 0o777
	if permissions == 0 {
		return 0o700
	}
	return permissions
}

func backupFileMode(mode int64) os.FileMode {
	permissions := os.FileMode(mode) & 0o666
	if permissions == 0 {
		return 0o600
	}
	return permissions
}

func validateServerID(serverID string) error {
	if !backupIDPattern.MatchString(serverID) {
		return fmt.Errorf("invalid server id")
	}
	return nil
}

func restoreMarkerPath(root, serverID string) string {
	return filepath.Join(root, ".gugu-restore-"+serverID+".json")
}

func writeRestoreMarker(root string, marker restoreMarker) error {
	if marker.Version != restoreMarkerVersion || validateServerID(marker.ServerID) != nil {
		return fmt.Errorf("invalid restore marker")
	}
	for _, name := range []string{marker.Staging, marker.Previous} {
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("invalid restore marker path")
		}
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(root, ".gugu-restore-marker-*.partial")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	target := restoreMarkerPath(root, marker.ServerID)
	if err := os.Rename(tempPath, target); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if err := os.Rename(tempPath, target); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}

func readRestoreMarker(markerPath string) (restoreMarker, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return restoreMarker{}, err
	}
	var marker restoreMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return restoreMarker{}, fmt.Errorf("decode restore marker: %w", err)
	}
	if marker.Version != restoreMarkerVersion || validateServerID(marker.ServerID) != nil {
		return restoreMarker{}, fmt.Errorf("invalid restore marker")
	}
	for _, name := range []string{marker.Staging, marker.Previous} {
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return restoreMarker{}, fmt.Errorf("invalid restore marker path")
		}
	}
	return marker, nil
}

func pathExists(filePath string) (bool, error) {
	_, err := os.Lstat(filePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func recoverRestoreMarker(root, markerPath string) error {
	marker, err := readRestoreMarker(markerPath)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(root, marker.ServerID)
	staging := filepath.Join(root, marker.Staging)
	previous := filepath.Join(root, marker.Previous)
	dataExists, err := pathExists(dataDir)
	if err != nil {
		return err
	}
	stagingExists, err := pathExists(staging)
	if err != nil {
		return err
	}
	previousExists, err := pathExists(previous)
	if err != nil {
		return err
	}

	if dataExists {
		if previousExists {
			if err := os.RemoveAll(previous); err != nil {
				return err
			}
		}
		if stagingExists {
			if err := os.RemoveAll(staging); err != nil {
				return err
			}
		}
	} else if previousExists {
		if err := os.Rename(previous, dataDir); err != nil {
			return fmt.Errorf("rollback interrupted restore: %w", err)
		}
		if stagingExists {
			if err := os.RemoveAll(staging); err != nil {
				return err
			}
		}
	} else if stagingExists && marker.Phase == "old-moved" {
		if err := os.Rename(staging, dataDir); err != nil {
			return fmt.Errorf("complete interrupted restore: %w", err)
		}
	} else {
		return fmt.Errorf("restore marker has no recoverable server data")
	}
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(root)
}

func recoverInterruptedRestores(dataRoot string) error {
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return err
	}
	markers, err := filepath.Glob(filepath.Join(root, ".gugu-restore-*.json"))
	if err != nil {
		return err
	}
	for _, markerPath := range markers {
		if err := recoverRestoreMarker(root, markerPath); err != nil {
			return fmt.Errorf("recover %s: %w", filepath.Base(markerPath), err)
		}
	}
	return nil
}

func replaceServerDataFromBackup(dataRoot, containerName, archivePath, expectedManifest string) error {
	const prefix = "gugu-server-"
	serverID := strings.TrimPrefix(containerName, prefix)
	if serverID == containerName || validateServerID(serverID) != nil {
		return fmt.Errorf("invalid server container name")
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return err
	}
	if err := recoverInterruptedRestores(root); err != nil {
		return err
	}
	dataDir := filepath.Join(root, serverID)
	if relative, err := filepath.Rel(root, dataDir); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("server data directory escapes data root")
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return fmt.Errorf("inspect current server data: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("current server data is not a directory")
	}
	staging, err := os.MkdirTemp(root, "."+serverID+".restore-")
	if err != nil {
		return fmt.Errorf("create restore staging directory: %w", err)
	}
	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(staging)
		}
	}()
	manifest, err := extractBackupArchive(archivePath, staging)
	if err != nil {
		return err
	}
	if expectedManifest != "" && manifest != expectedManifest {
		return fmt.Errorf("%w: manifest digest mismatch", errBackupIntegrity)
	}
	if err := syncDirectory(staging); err != nil {
		return err
	}
	previous := staging + ".previous"
	marker := restoreMarker{
		Version:  restoreMarkerVersion,
		ServerID: serverID,
		Staging:  filepath.Base(staging),
		Previous: filepath.Base(previous),
		Phase:    "prepared",
	}
	if err := writeRestoreMarker(root, marker); err != nil {
		return fmt.Errorf("write restore marker: %w", err)
	}
	if err := os.Rename(dataDir, previous); err != nil {
		_ = os.Remove(restoreMarkerPath(root, serverID))
		return fmt.Errorf("move current server data aside: %w", err)
	}
	marker.Phase = "old-moved"
	if err := writeRestoreMarker(root, marker); err != nil {
		_ = os.Rename(previous, dataDir)
		return fmt.Errorf("update restore marker: %w", err)
	}
	if err := os.Rename(staging, dataDir); err != nil {
		rollbackErr := os.Rename(previous, dataDir)
		if rollbackErr == nil {
			_ = os.Remove(restoreMarkerPath(root, serverID))
		}
		if rollbackErr != nil {
			return fmt.Errorf("activate restored data: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("activate restored data: %w", err)
	}
	activated = true
	marker.Phase = "activated"
	if err := writeRestoreMarker(root, marker); err != nil {
		// The filesystem layout is authoritative. A subsequent startup can infer
		// successful activation from dataDir + previous even with the older phase.
		return fmt.Errorf("persist activated restore marker: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	if err := os.RemoveAll(previous); err != nil {
		// Activation already succeeded. Keep the marker so startup recovery can
		// retry cleanup without turning a successful restore into a failed task.
		return nil
	}
	if err := os.Remove(restoreMarkerPath(root, serverID)); err != nil && !os.IsNotExist(err) {
		return nil
	}
	return syncDirectory(root)
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}
