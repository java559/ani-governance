package permcode

const ComputeCatalogPath = "/api/v1/compute-presets"

// ComputeCatalogGrant returns the business entitlement and exact directory
// permission. A shared HTTP path is never sufficient to authorize another usage.
func ComputeCatalogGrant(usage string) (module, code string, ok bool) {
	switch usage {
	case "SANDBOX":
		return "INSTANCE", "sandbox:preset:read", true
	case "CONTAINER_INSTANCE":
		return "INSTANCE", "instance:preset:read", true
	case "TRAINING":
		return "MODEL", "modeldev:compute-preset:read", true
	case "INFERENCE":
		return "INFERENCE", "inference:compute-preset:read", true
	case "VIRTUAL_MACHINE":
		return "VIRTUAL_MACHINE", "vm:compute-preset:read", true
	default:
		return "", "", false
	}
}
