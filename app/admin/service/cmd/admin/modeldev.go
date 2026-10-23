package main

import (
	"errors"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	"io"
	"os"
)

// The shared authenticator reports invalid startup configuration by panicking.
// Bound that constructor only, without exposing keys or recovering use-case bugs.
func newModelDevAuthenticator(ctx *bootstrap.Context, cache *data.UserTokenCache) (value *data.Authenticator, err error) {
	defer func() {
		if recover() != nil {
			value, err = nil, errors.New("modeldev JWT configuration invalid")
		}
	}()
	return data.NewAuthenticator(ctx, cache), nil
}

func readModelDevPrivateFile(path string, limit int64) ([]byte, error) {
	invalid := errors.New("invalid private file")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() == 0 || before.Size() > limit {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, invalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, invalid
	}
	return raw, nil
}
