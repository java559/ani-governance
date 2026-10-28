package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
)

// The owner hands out a short-lived URL on the object store's in-cluster
// address. That host resolves only inside the cluster and presents a
// certificate from the internal root, so a browser cannot use it. This route
// fetches the object with the same trust the BFF already holds and relays the
// bytes, which keeps the object store unexposed.
const modelDevArtifactDownloadPath = "/api/v1/modeldev/artifacts/{artifact_id}/download"

const (
	artifactDownloadOpenTimeout = 20 * time.Second
	// The largest published artifact is a merged checkpoint of about 2 GiB.
	// The budget bounds a stuck transfer without blocking a real download.
	artifactDownloadLifetime = 30 * time.Minute
)

// artifactDownloadAuthorizer is the only capability this transport needs: the
// service resolves the caller's scope and the owner mints the URL. Narrowing it
// keeps the handler testable with a substituted upstream.
type artifactDownloadAuthorizer interface {
	AuthorizeArtifactDownload(context.Context, *modeldevv1.AuthorizeArtifactDownloadRequest) (*modeldevv1.AuthorizeArtifactDownloadResponse, error)
}

type artifactDownloadHTTP struct {
	authorizer artifactDownloadAuthorizer
	client     *http.Client
}

// RegisterModelDevArtifactDownloadHTTP exposes the byte relay. It is a separate
// route from the URL exchange so the existing JSON contract stays unchanged
// while clients that cannot reach the object store still have a way in.
func RegisterModelDevArtifactDownloadHTTP(server *khttp.Server, s *service.ModelDevService) {
	h := &artifactDownloadHTTP{client: artifactDownloadClient(os.Getenv("ANI_MODELDEV_CA"))}
	if s != nil {
		h.authorizer = s
	}
	server.Route("/").GET(modelDevArtifactDownloadPath, h.handle)
}

// artifactDownloadClient trusts only the internal root that also signs the
// owner's server certificate. Redirects are refused: a signed URL is answered
// directly, so a redirect can only move the request to a host this client was
// not configured to trust.
func artifactDownloadClient(caFile string) *http.Client {
	if caFile == "" {
		return nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots},
		// A download is already compressed where it matters; transparent
		// decompression would hide the real byte count from the client.
		DisableCompression:    true,
		ResponseHeaderTimeout: artifactDownloadOpenTimeout,
		MaxIdleConnsPerHost:   4,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (h *artifactDownloadHTTP) handle(ctx khttp.Context) error {
	modelDevQueryHeaders(ctx)
	khttp.SetOperation(ctx, "/admin.service.v1.ModelDevService/DownloadArtifact")
	request := ctx.Request()
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		return invalidModelDevQueryHTTP()
	}
	var in modeldevv1.AuthorizeArtifactDownloadRequest
	if ctx.BindVars(&in) != nil || !modelDevQueryCanonicalUUID(in.ArtifactId) {
		return invalidModelDevQueryHTTP()
	}
	handler := ctx.Middleware(func(call context.Context, value interface{}) (interface{}, error) {
		if h.authorizer == nil {
			return nil, kratoserrors.ServiceUnavailable("ARTIFACT_DOWNLOAD_UNAVAILABLE", "artifact download unavailable")
		}
		return h.authorizer.AuthorizeArtifactDownload(call, value.(*modeldevv1.AuthorizeArtifactDownloadRequest))
	})
	out, err := handler(ctx, &in)
	if err != nil {
		return artifactDownloadPreStreamError(err)
	}
	grant, ok := out.(*modeldevv1.AuthorizeArtifactDownloadResponse)
	if !ok || grant == nil {
		return kratoserrors.ServiceUnavailable("ARTIFACT_DOWNLOAD_UNAVAILABLE", "artifact download unavailable")
	}
	target, err := artifactDownloadTarget(grant.DownloadUrl)
	if err != nil {
		return kratoserrors.ServiceUnavailable("ARTIFACT_DOWNLOAD_UNAVAILABLE", "artifact download unavailable")
	}
	return h.relay(ctx, target, grant)
}

// artifactDownloadTarget accepts only an absolute https URL. The owner already
// authorizes the artifact, so the remaining checks guard the transport: a
// plaintext or relative target would either leak the grant or fail obscurely.
func artifactDownloadTarget(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("invalid artifact url")
	}
	target, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid artifact url")
	}
	if target.Scheme != "https" || target.Host == "" || target.User != nil {
		return nil, fmt.Errorf("invalid artifact url")
	}
	return target, nil
}

func (h *artifactDownloadHTTP) relay(ctx khttp.Context, target *url.URL, grant *modeldevv1.AuthorizeArtifactDownloadResponse) error {
	call, cancel := context.WithTimeout(ctx.Request().Context(), artifactDownloadLifetime)
	defer cancel()
	return h.copyArtifact(call, ctx.Response(), target, grant)
}

// copyArtifact fetches the object and relays it. Nothing is written before the
// upstream status is known, so an unreachable or untrusted store still produces
// a real HTTP error instead of a truncated body.
func (h *artifactDownloadHTTP) copyArtifact(call context.Context, writer http.ResponseWriter, target *url.URL, grant *modeldevv1.AuthorizeArtifactDownloadResponse) error {
	if h.client == nil {
		return kratoserrors.ServiceUnavailable("ARTIFACT_DOWNLOAD_UNAVAILABLE", "artifact download unavailable")
	}
	request, err := http.NewRequestWithContext(call, http.MethodGet, target.String(), nil)
	if err != nil {
		return kratoserrors.ServiceUnavailable("ARTIFACT_DOWNLOAD_UNAVAILABLE", "artifact download unavailable")
	}
	response, err := h.client.Do(request)
	if err != nil {
		if call.Err() != nil {
			return kratoserrors.New(http.StatusGatewayTimeout, "ARTIFACT_DOWNLOAD_TIMEOUT", "artifact download timed out")
		}
		return kratoserrors.New(http.StatusBadGateway, "ARTIFACT_DOWNLOAD_FAILED", "artifact download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return kratoserrors.New(http.StatusBadGateway, "ARTIFACT_DOWNLOAD_FAILED", "artifact download failed")
	}
	writer.Header().Set("Content-Type", artifactDownloadContentType(response.Header.Get("Content-Type")))
	if response.ContentLength >= 0 {
		writer.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	writer.Header().Set("Content-Disposition", artifactDownloadDisposition(grant.GetArtifact().GetFilename()))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	// A client that walks away cancels the call, which stops the copy instead
	// of draining the rest of the object.
	_, _ = io.Copy(writer, response.Body)
	return nil
}

// artifactDownloadContentType keeps the object store's type unless it is one a
// browser could execute in the console's origin.
func artifactDownloadContentType(value string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	switch mediaType {
	case "application/octet-stream", "text/plain", "application/json", "application/x-ndjson":
		return value
	default:
		return "application/octet-stream"
	}
}

// artifactDownloadDisposition keeps only the base name: the artifact filename
// is a relative path, and a path in a header would suggest a nested write.
func artifactDownloadDisposition(filename string) string {
	name := path.Base(strings.TrimSpace(filename))
	if name == "." || name == "/" || name == "" {
		name = "artifact"
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '\\' || r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, name)
	if cleaned == "" {
		cleaned = "artifact"
	}
	return `attachment; filename="` + cleaned + `"`
}

// artifactDownloadPreStreamError preserves the reviewed reasons the JSON
// exchange already produces, so both routes fail the same way.
func artifactDownloadPreStreamError(err error) error {
	switch kratoserrors.FromError(err).Code {
	case http.StatusBadRequest:
		return invalidModelDevQueryHTTP()
	case http.StatusNotFound, http.StatusForbidden:
		return kratoserrors.NotFound("RESOURCE_NOT_FOUND", "resource unavailable")
	case http.StatusPreconditionFailed:
		return kratoserrors.New(http.StatusPreconditionFailed, "ARTIFACT_NOT_PUBLISHED", "artifact unavailable")
	default:
		return err
	}
}
