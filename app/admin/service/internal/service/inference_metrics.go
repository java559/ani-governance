package service

import (
	"context"
	"net/url"
	"strings"
	"time"

	view "go-wind-admin/api/gen/go/inference/service/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runtime identities are resolved over authenticated owner RPC, never supplied
// as public metric selectors. The reader queries only those exact Pod UIDs.
type InferenceMetricsReader interface {
	Query(context.Context, *view.InferenceMetricsRequest, []*view.RuntimeInstance) (*view.InferenceMetricsResponse, error)
}

func (s *InferenceService) ConfigureDiagnostics(endpoint string, metrics InferenceMetricsReader) error {
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v1/completions") {
			return status.Error(codes.InvalidArgument, "invalid managed invocation endpoint")
		}
	}
	s.invocationEndpoint, s.metrics = endpoint, metrics
	return nil
}

func (s *InferenceService) GetInferenceMetrics(ctx context.Context, in *view.InferenceMetricsRequest) (*view.InferenceMetricsResponse, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "metrics request required")
	}
	now := time.Now().UTC()
	if in.End == nil {
		in.End = timestamppb.New(now)
	}
	if in.Start == nil {
		in.Start = timestamppb.New(in.End.AsTime().Add(-time.Hour))
	}
	if in.Step == 0 {
		in.Step = 60
	}
	if in.Start.CheckValid() != nil || in.End.CheckValid() != nil || in.Step < 15 || in.Step > 3600 {
		return nil, status.Error(codes.InvalidArgument, "invalid metrics range or step (15..3600 seconds)")
	}
	span := in.End.AsTime().Sub(in.Start.AsTime())
	if span <= 0 || span > 24*time.Hour || span/time.Second/time.Duration(in.Step) > 1440 || in.End.AsTime().After(now.Add(time.Minute)) {
		return nil, status.Error(codes.InvalidArgument, "metrics range must be positive, at most 24 hours and 1440 intervals")
	}
	if s.metrics == nil {
		// Keep trusted identity and tenant checks, without probing the owner
		// when no metrics source can serve the request.
		if _, _, _, err := s.diagnosticsScope(ctx, in.ResourceId); err != nil {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "metrics source is not configured")
	}
	runtime, err := s.GetRuntimeDiagnostics(ctx, &view.GetRuntimeDiagnosticsRequest{ResourceId: in.ResourceId})
	if err != nil {
		return nil, err
	}
	out, err := s.metrics.Query(ctx, in, runtime.Instances)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "metrics source unavailable")
	}
	return out, nil
}
