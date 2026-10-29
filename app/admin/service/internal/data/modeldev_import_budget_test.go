package data

import "testing"

func TestModelDevImportCannotIgnoreInvalidBudget(t *testing.T) {
	clearModelDevConfigEnvironment(t)
	setModelDevConfigConnection(t)
	t.Setenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS", "0")
	_, err := ModelDevConfigFromEnv()
	if err == nil {
		t.Fatal("MODELDEV_IMPORT_DEADLINE: configured zero import deadline must fail closed")
	}
}
