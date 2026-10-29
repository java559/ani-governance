package data

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// ModelDevConfigFromEnv reads only the explicit Governance-to-ModelDev
// connection settings. Empty configuration disables new remote resolution;
// accepted-key lookup does not require this client. Certificate material and
// the fixed server identity are validated by NewModelDevClient.
func ModelDevConfigFromEnv() (ModelDevClientConfig, error) {
	config := ModelDevClientConfig{
		Address:  os.Getenv("ANI_MODELDEV_ADDR"),
		CAFile:   os.Getenv("ANI_MODELDEV_CA"),
		CertFile: os.Getenv("ANI_MODELDEV_CERT"),
		KeyFile:  os.Getenv("ANI_MODELDEV_KEY"),
	}
	timeout := os.Getenv("ANI_MODELDEV_TIMEOUT")
	resolution := os.Getenv("ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS")
	if config == (ModelDevClientConfig{}) && timeout == "" && resolution == "" && os.Getenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS") == "" {
		return ModelDevClientConfig{}, nil
	}
	invalid := errors.New("invalid modeldev environment configuration")
	for _, value := range []string{config.Address, config.CAFile, config.CertFile, config.KeyFile} {
		if value == "" || strings.TrimSpace(value) != value {
			return ModelDevClientConfig{}, invalid
		}
	}
	config.Timeout = 3 * time.Second
	config.ModelResolutionTimeout = 300 * time.Second
	var err error
	config.ImportTimeout, err = ModelDevImportTimeoutFromEnv()
	if err != nil {
		return ModelDevClientConfig{}, invalid
	}
	if timeout != "" {
		parsed, err := time.ParseDuration(timeout)
		if err != nil || parsed <= 0 || parsed > 24*time.Hour {
			return ModelDevClientConfig{}, invalid
		}
		config.Timeout = parsed
	}
	if resolution != "" {
		seconds, err := strconv.ParseInt(resolution, 10, 64)
		if err != nil || seconds <= 0 || seconds > math.MaxUint32 || strconv.FormatInt(seconds, 10) != resolution {
			return ModelDevClientConfig{}, invalid
		}
		config.ModelResolutionTimeout = time.Duration(seconds) * time.Second
	}
	return config, nil
}

// ModelDevImportTimeoutFromEnv is also read before the management CLI starts
// its authenticated import, so every layer shares one remaining deadline.
func ModelDevImportTimeoutFromEnv() (time.Duration, error) {
	value := os.Getenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS")
	if value == "" {
		return 1800 * time.Second, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > math.MaxUint32 || strconv.FormatInt(seconds, 10) != value {
		return 0, errors.New("invalid modeldev import timeout configuration")
	}
	return time.Duration(seconds) * time.Second, nil
}
