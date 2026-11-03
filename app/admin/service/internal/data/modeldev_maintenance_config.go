package data

import (
	"errors"
	"os"
)

// ModelDevCreatePausedFromEnv affects only new ModelDev Create admission and
// publisher claims. It is independent of connection and Stop/release settings.
func ModelDevCreatePausedFromEnv() (bool, error) {
	switch os.Getenv("ANI_MODELDEV_CREATE_PAUSED") {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New("invalid ANI_MODELDEV_CREATE_PAUSED configuration")
	}
}
