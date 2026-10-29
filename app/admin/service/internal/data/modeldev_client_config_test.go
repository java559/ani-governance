package data

import (
	"testing"
	"time"
)

func TestModelDevConfigFromEnvDisablesUnconfiguredResolution(t *testing.T) {
	clearModelDevConfigEnvironment(t)
	got, err := ModelDevConfigFromEnv()
	if err != nil || got != (ModelDevClientConfig{}) {
		t.Fatalf("unconfigured resolution must be disabled without an error: config=%+v err=%v", got, err)
	}
}

func TestModelDevConfigFromEnvLoadsExplicitConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{name: "default timeout", want: 3 * time.Second},
		{name: "explicit timeout", timeout: "1250ms", want: 1250 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearModelDevConfigEnvironment(t)
			setModelDevConfigConnection(t)
			t.Setenv("ANI_MODELDEV_TIMEOUT", tc.timeout)
			got, err := ModelDevConfigFromEnv()
			want := ModelDevClientConfig{
				Address:                "modeldev.internal:9443",
				CAFile:                 "/managed/modeldev/ca.pem",
				CertFile:               "/managed/modeldev/client.pem",
				KeyFile:                "/managed/modeldev/client.key",
				Timeout:                tc.want,
				ModelResolutionTimeout: 300 * time.Second,
				ImportTimeout:          1800 * time.Second,
			}
			if err != nil || got != want {
				t.Fatalf("explicit connection settings must be loaded without reading certificate files: got=%+v want=%+v err=%v", got, want, err)
			}
		})
	}
}

func TestModelDevConfigFromEnvRejectsPartialConnection(t *testing.T) {
	for _, missing := range []string{"ANI_MODELDEV_ADDR", "ANI_MODELDEV_CA", "ANI_MODELDEV_CERT", "ANI_MODELDEV_KEY"} {
		t.Run(missing, func(t *testing.T) {
			clearModelDevConfigEnvironment(t)
			setModelDevConfigConnection(t)
			t.Setenv(missing, "")
			got, err := ModelDevConfigFromEnv()
			if err == nil || got != (ModelDevClientConfig{}) {
				t.Fatalf("partial configuration must fail without a usable client configuration: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestModelDevConfigFromEnvRejectsInvalidTimeout(t *testing.T) {
	for _, timeout := range []string{"not-a-duration", "0s", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			clearModelDevConfigEnvironment(t)
			setModelDevConfigConnection(t)
			t.Setenv("ANI_MODELDEV_TIMEOUT", timeout)
			got, err := ModelDevConfigFromEnv()
			if err == nil || got != (ModelDevClientConfig{}) {
				t.Fatalf("invalid timeout must fail without a usable client configuration: got=%+v err=%v", got, err)
			}
		})
	}
}

func clearModelDevConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ANI_MODELDEV_ADDR", "ANI_MODELDEV_CA", "ANI_MODELDEV_CERT", "ANI_MODELDEV_KEY", "ANI_MODELDEV_TIMEOUT", "ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS", "ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
	}
}

func TestModelDevImportTimeoutConfigurationUsesIndependentFiniteSeconds(t *testing.T) {
	for _, value := range []string{"0", "-1", "1800s", "1.5", " 2", "+2", "02", "4294967296"} {
		t.Run(value, func(t *testing.T) {
			clearModelDevConfigEnvironment(t)
			setModelDevConfigConnection(t)
			t.Setenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS", value)
			_, err := ModelDevConfigFromEnv()
			if err == nil {
				t.Fatal("invalid import timeout accepted")
			}
		})
	}
	clearModelDevConfigEnvironment(t)
	setModelDevConfigConnection(t)
	t.Setenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS", "2400")
	got, err := ModelDevConfigFromEnv()
	if err != nil || got.ImportTimeout != 2400*time.Second || got.Timeout != 3*time.Second || got.ModelResolutionTimeout != 300*time.Second {
		t.Fatal("import budget must not replace short query or model resolution budgets")
	}
	t.Setenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS", "4294967295")
	t.Setenv("ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS", "4294967295")
	got, err = ModelDevConfigFromEnv()
	if err != nil || got.ImportTimeout != 4294967295*time.Second || got.ModelResolutionTimeout != 4294967295*time.Second {
		t.Fatal("Gov must accept the same positive uint32-second range as ModelDev")
	}
}

func TestModelDevResolutionTimeoutConfigurationIsPositiveSeconds(t *testing.T) {
	for _, value := range []string{"0", "-1", "1s", "1.5", " 2", "+2", "02", "4294967296"} {
		t.Run(value, func(t *testing.T) {
			clearModelDevConfigEnvironment(t)
			setModelDevConfigConnection(t)
			t.Setenv("ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS", value)
			_, err := ModelDevConfigFromEnv()
			if err == nil {
				t.Fatal("invalid model resolution timeout accepted")
			}
		})
	}
}

func setModelDevConfigConnection(t *testing.T) {
	t.Helper()
	t.Setenv("ANI_MODELDEV_ADDR", "modeldev.internal:9443")
	t.Setenv("ANI_MODELDEV_CA", "/managed/modeldev/ca.pem")
	t.Setenv("ANI_MODELDEV_CERT", "/managed/modeldev/client.pem")
	t.Setenv("ANI_MODELDEV_KEY", "/managed/modeldev/client.key")
}
