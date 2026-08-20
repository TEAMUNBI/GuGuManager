package gamedefinition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	APIVersionV1Alpha1 = "gugumanager.io/games/v1alpha1"
	APIVersionV1Beta1  = "gugumanager.io/games/v1beta1"
)

var lowerSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var prefixedSHA256 = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type BundleRevisionMetadata struct {
	Artifacts  []ArtifactManifestEntry `json:"artifacts"`
	Extensions []ExtensionDescriptor   `json:"extensions,omitempty"`
	SBOM       *ContentDescriptor      `json:"sbom,omitempty"`
	Provenance *ContentDescriptor      `json:"provenance,omitempty"`
}

type ArtifactManifestEntry struct {
	URL               string `json:"url"`
	Destination       string `json:"destination"`
	SHA256            string `json:"sha256"`
	SizeBytes         int64  `json:"sizeBytes"`
	UnpackedSizeBytes int64  `json:"unpackedSizeBytes,omitempty"`
	MediaType         string `json:"mediaType"`
}

type ExtensionDescriptor struct {
	ID             string   `json:"id"`
	ArtifactSHA256 string   `json:"artifactSha256"`
	ABI            string   `json:"abi"`
	Entrypoint     string   `json:"entrypoint"`
	Permissions    []string `json:"permissions,omitempty"`
}

type ContentDescriptor struct {
	Format         string `json:"format"`
	ArtifactSHA256 string `json:"artifactSha256"`
}

func decodeOneJSON(content []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return document, nil
}

// ValidateJSON accepts the current schema and the previous stable schema.
// v1alpha1 documents are never mutated by validation; conversion happens only
// at an explicit normalization boundary.
func ValidateJSON(content []byte) error {
	document, err := decodeOneJSON(content)
	if err != nil {
		return err
	}
	switch document["apiVersion"] {
	case APIVersionV1Alpha1:
		return ValidateV1Alpha1(document)
	case APIVersionV1Beta1:
		return validateV1Beta1Document(document)
	default:
		return fmt.Errorf("unsupported GameDefinition apiVersion %q", document["apiVersion"])
	}
}

func ValidateV1Beta1JSON(content []byte) error {
	document, err := decodeOneJSON(content)
	if err != nil {
		return err
	}
	return validateV1Beta1Document(document)
}

func validateV1Beta1Document(document map[string]any) error {
	if document["apiVersion"] != APIVersionV1Beta1 {
		return fmt.Errorf("apiVersion must be %s", APIVersionV1Beta1)
	}
	// Reuse the complete v1alpha1 structural schema after removing only the
	// v1beta1 addition. This makes conversion and validation deterministic and
	// prevents the two versions from drifting on shared runtime semantics.
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	var alpha map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&alpha); err != nil {
		return err
	}
	alpha["apiVersion"] = APIVersionV1Alpha1
	spec, ok := alpha["spec"].(map[string]any)
	if !ok {
		return errors.New("spec must be an object")
	}
	bundleValue, exists := spec["bundle"]
	if !exists {
		return errors.New("spec.bundle is required in v1beta1")
	}
	delete(spec, "bundle")
	if err := ValidateV1Alpha1(alpha); err != nil {
		return err
	}
	return validateRevisionMetadata(bundleValue, spec)
}

func validateRevisionMetadata(value any, spec map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return errors.New("spec.bundle must be an object")
	}
	allowed := map[string]bool{"artifacts": true, "extensions": true, "sbom": true, "provenance": true}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("spec.bundle contains unknown property %q", key)
		}
	}
	if _, ok := raw["artifacts"]; !ok {
		return errors.New("spec.bundle.artifacts is required")
	}
	var metadata BundleRevisionMetadata
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return fmt.Errorf("decode spec.bundle: %w", err)
	}
	seenDestinations := map[string]bool{}
	for index, artifact := range metadata.Artifacts {
		if !strings.HasPrefix(artifact.URL, "https://") || artifact.Destination == "" || !lowerSHA256.MatchString(artifact.SHA256) || artifact.SizeBytes < 0 || artifact.UnpackedSizeBytes < 0 || artifact.MediaType == "" {
			return fmt.Errorf("spec.bundle.artifacts[%d] is incomplete or invalid", index)
		}
		if seenDestinations[artifact.Destination] {
			return fmt.Errorf("spec.bundle.artifacts[%d].destination duplicates %q", index, artifact.Destination)
		}
		seenDestinations[artifact.Destination] = true
	}
	allowedPermissions := map[string]bool{
		"server-data-read": true, "server-data-write": true, "https": true, "progress": true, "secret-bind": true,
	}
	seenExtensions := map[string]bool{}
	for index, extension := range metadata.Extensions {
		if extension.ID == "" || seenExtensions[extension.ID] || !lowerSHA256.MatchString(extension.ArtifactSHA256) || extension.ABI != "wasi-component/v1" || extension.Entrypoint == "" {
			return fmt.Errorf("spec.bundle.extensions[%d] is incomplete or invalid", index)
		}
		seenExtensions[extension.ID] = true
		seenPermissions := map[string]bool{}
		for _, permission := range extension.Permissions {
			if !allowedPermissions[permission] || seenPermissions[permission] {
				return fmt.Errorf("spec.bundle.extensions[%d] declares invalid permission %q", index, permission)
			}
			seenPermissions[permission] = true
		}
	}
	for name, descriptor := range map[string]*ContentDescriptor{"sbom": metadata.SBOM, "provenance": metadata.Provenance} {
		if descriptor != nil && (descriptor.Format == "" || !lowerSHA256.MatchString(descriptor.ArtifactSHA256)) {
			return fmt.Errorf("spec.bundle.%s is incomplete or invalid", name)
		}
	}
	return crossCheckArtifacts(metadata.Artifacts, spec)
}

func crossCheckArtifacts(manifest []ArtifactManifestEntry, spec map[string]any) error {
	install, _ := spec["install"].(map[string]any)
	declared, _ := install["artifacts"].([]any)
	manifestByDestination := make(map[string]ArtifactManifestEntry, len(manifest))
	for _, artifact := range manifest {
		manifestByDestination[artifact.Destination] = artifact
	}
	for index, value := range declared {
		artifact, _ := value.(map[string]any)
		destination, _ := artifact["destination"].(string)
		sha, _ := artifact["sha256"].(string)
		entry, ok := manifestByDestination[destination]
		if !ok || entry.SHA256 != sha {
			return fmt.Errorf("spec.install.artifacts[%d] is not pinned by spec.bundle.artifacts", index)
		}
	}
	return nil
}

// NormalizeToV1Beta1JSON deterministically converts the previous stable
// version into the current internal form. Unknown-size alpha artifacts use 0;
// the Agent still enforces configured transfer limits and SHA-256.
func NormalizeToV1Beta1JSON(content []byte) ([]byte, error) {
	if err := ValidateJSON(content); err != nil {
		return nil, err
	}
	document, err := decodeOneJSON(content)
	if err != nil {
		return nil, err
	}
	if document["apiVersion"] == APIVersionV1Alpha1 {
		document["apiVersion"] = APIVersionV1Beta1
		spec := document["spec"].(map[string]any)
		install, _ := spec["install"].(map[string]any)
		declared, _ := install["artifacts"].([]any)
		artifacts := make([]any, 0, len(declared))
		for _, value := range declared {
			artifact, _ := value.(map[string]any)
			artifacts = append(artifacts, map[string]any{
				"url": artifact["url"], "destination": artifact["destination"], "sha256": artifact["sha256"],
				"sizeBytes": json.Number("0"), "mediaType": "application/octet-stream",
			})
		}
		spec["bundle"] = map[string]any{"artifacts": artifacts}
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if err := ValidateV1Beta1JSON(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func SortedExtensionPermissions(permissions []string) []string {
	result := append([]string(nil), permissions...)
	sort.Strings(result)
	return result
}

func ValidPrefixedSHA256(value string) bool { return prefixedSHA256.MatchString(value) }
