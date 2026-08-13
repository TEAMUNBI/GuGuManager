package domain

import "strings"

const (
	NodeCapabilityRuntimeContainer = "runtime.container/v1"
	NodeCapabilityServerReconcile  = "server.reconcile/v1"
)

// CanonicalNodeCapability converts the protocol's separate name/version
// declaration into the single stable string exposed by the HTTP/domain model.
// Legacy container declarations are accepted only as input and normalized to
// runtime.container/v1; new writes never persist the legacy spelling.
func CanonicalNodeCapability(name string, version string) (string, bool) {
	name = strings.TrimSpace(name)
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if name == "" {
		return "", false
	}

	if version == "" {
		var ok bool
		name, version, ok = splitCanonicalCapability(name)
		if !ok {
			// Older in-process callers supplied only a capability name. Preserve
			// compatibility by treating that declaration as version 1.
			version = "1"
		}
	}

	switch name {
	case "container", "container/v1", "runtime.docker", "runtime.docker/v1", "runtime.container/v1":
		name = "runtime.container"
		version = "1"
	case "server.reconcile/v1":
		name = "server.reconcile"
		version = "1"
	}
	if version == "" || strings.ContainsAny(name, " \t\r\n") || strings.ContainsAny(version, " \t\r\n/") {
		return "", false
	}
	return name + "/v" + version, true
}

// SplitNodeCapability returns the database/protocol name and version for a
// canonical (or supported legacy) declaration.
func SplitNodeCapability(declaration string) (name string, version string, ok bool) {
	canonical, ok := CanonicalNodeCapability(declaration, "")
	if !ok {
		return "", "", false
	}
	name, version, ok = splitCanonicalCapability(canonical)
	return name, version, ok
}

func splitCanonicalCapability(declaration string) (name string, version string, ok bool) {
	declaration = strings.TrimSpace(declaration)
	separator := strings.LastIndex(declaration, "/v")
	if separator <= 0 || separator+2 >= len(declaration) {
		return declaration, "", false
	}
	return declaration[:separator], declaration[separator+2:], true
}

func NodePlatformCapability(platform string) string {
	platform = strings.TrimSpace(strings.ToLower(platform))
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "platform." + parts[0] + "." + parts[1] + "/v1"
}
