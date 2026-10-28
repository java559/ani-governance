package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
)

// The object store answers only over https with the internal root. A plaintext
// or credential-bearing target must be refused before any request is made.
func TestArtifactDownloadTarget(t *testing.T) {
	accepted := []string{
		"https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000/bucket/object?X-Amz-Signature=abc",
		"https://store.example/object",
	}
	rejected := []string{
		"",
		" http://store.example/object",
		"http://store.example/object",
		"ftp://store.example/object",
		"/object",
		"https://user:pass@store.example/object",
		"https:///object",
	}
	for _, value := range accepted {
		if _, err := artifactDownloadTarget(value); err != nil {
			t.Fatalf("rejected a usable target %q: %v", value, err)
		}
	}
	for _, value := range rejected {
		if _, err := artifactDownloadTarget(value); err == nil {
			t.Fatalf("accepted an unusable target %q", value)
		}
	}
}

// A browser must never be told to render an artifact inline in the console's
// origin, and a path must not reach the header.
func TestArtifactDownloadHeaders(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{"application/octet-stream", "application/octet-stream"},
		{"application/json", "application/json"},
		{"text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		{"text/html", "application/octet-stream"},
		{"image/svg+xml", "application/octet-stream"},
		{"", "application/octet-stream"},
	} {
		require.Equal(t, test.want, artifactDownloadContentType(test.value), test.value)
	}
	require.Equal(t,
		`attachment; filename="model.safetensors"`,
		artifactDownloadDisposition("export/merged/model.safetensors"))
	require.Equal(t, `attachment; filename="artifact"`, artifactDownloadDisposition(""))
	// path.Base already drops a trailing separator, so the last segment wins.
	require.Equal(t, `attachment; filename="merged"`, artifactDownloadDisposition("export/merged/"))
	require.Equal(t,
		`attachment; filename="Set-Cookie: xy"`,
		artifactDownloadDisposition("export/\r\nSet-Cookie: x\"y"))
}

// The relay must copy the object bytes and honor the declared length, so a
// client sees a real download rather than a chunked stream of unknown size.
func TestArtifactDownloadRelayCopiesBytes(t *testing.T) {
	payload := strings.Repeat("checkpoint-bytes;", 512)
	store := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/object" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = io.WriteString(w, payload)
	}))
	defer store.Close()

	h := &artifactDownloadHTTP{
		client: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    store.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
			}},
		},
	}
	target, err := artifactDownloadTarget(store.URL + "/bucket/object")
	require.NoError(t, err)
	grant := &modeldevv1.AuthorizeArtifactDownloadResponse{
		Artifact: &modeldevv1.ArtifactView{ArtifactId: "11111111-1111-4111-8111-111111111111", Filename: "export/merged/model.safetensors"},
	}
	recorder := httptest.NewRecorder()
	require.NoError(t, h.copyArtifact(context.Background(), recorder, target, grant))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, strconv.Itoa(len(payload)), recorder.Header().Get("Content-Length"))
	require.Equal(t, `attachment; filename="model.safetensors"`, recorder.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Equal(t, payload, recorder.Body.String())
}

// A store that answers with an error must not look like a short download; the
// caller gets a status it can act on and no bytes.
func TestArtifactDownloadRelayRejectsUpstreamFailure(t *testing.T) {
	store := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error>expired</Error>")
	}))
	defer store.Close()
	h := &artifactDownloadHTTP{
		client: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    store.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
			}},
		},
	}
	target, err := artifactDownloadTarget(store.URL + "/bucket/object")
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	require.Error(t, h.copyArtifact(context.Background(), recorder, target, &modeldevv1.AuthorizeArtifactDownloadResponse{}))
	require.Zero(t, recorder.Body.Len())
}

// Without a configured trust root the relay must fail closed rather than fall
// back to an unverified connection.
func TestArtifactDownloadClientRequiresTrust(t *testing.T) {
	require.Nil(t, artifactDownloadClient(""))
	require.Nil(t, artifactDownloadClient("/nonexistent/ca.pem"))
}
