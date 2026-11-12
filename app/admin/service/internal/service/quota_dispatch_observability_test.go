package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/tests/testutil"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

type loggingQuotaAdapter struct{}

func (loggingQuotaAdapter) OwnerService() string { return "quota-log-test-owner" }
func (loggingQuotaAdapter) Actions() []string    { return []string{"CREATE"} }
func (loggingQuotaAdapter) Dispatch(context.Context, *QuotaDispatchCommand) ([]byte, error) {
	return nil, nil
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record), line)
		out = append(out, record)
	}
	return out
}

func findLogRecord(records []map[string]any, event string) map[string]any {
	for _, record := range records {
		if record["event"] == event {
			return record
		}
	}
	return nil
}

// The database is unreachable, so both the charge read and the following
// MarkUnknown write fail: the original driver cause and the separate
// state.persist_failed must both be recorded on the restored operation trace.
func TestQuotaDispatchReadAndWriteFailuresAreLogged(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	db, err := sql.Open("pgx", "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	ledger := data.NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), entCrud.NewEntClient(client, driver))

	buf := &bytes.Buffer{}
	worker := &QuotaDispatchWorker{
		log:      bLogger.NewHelper(bLogger.NewJSONLogger(buf, slog.LevelDebug)),
		ledger:   ledger,
		registry: NewQuotaAdapterRegistry(),
	}
	require.NoError(t, worker.registry.Register(loggingQuotaAdapter{}))

	op := &data.ClaimedOperation{
		TenantID: 7, OperationID: "2b0f2f56-3f3a-4a2e-9c21-5f6f8f2a1b10",
		ResourceID: "resource-1", OwnerService: "quota-log-test-owner", Action: "CREATE",
		LeaseGeneration: 3,
		TelemetryJSON:   []byte(`{"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`),
	}
	worker.dispatchOne(context.Background(), op)

	records := logRecords(t, buf)
	readFailure := findLogRecord(records, "dependency.failed")
	require.NotNil(t, readFailure, "missing dependency.failed: %s", buf.String())
	require.Equal(t, "postgresql", readFailure["dependency"])
	require.Equal(t, "quota.original_charges.read", readFailure["action"])
	require.Equal(t, op.OperationID, readFailure["operation_id"])
	require.NotEmpty(t, readFailure["error"])
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", readFailure["trace_id"])

	persistFailure := findLogRecord(records, "state.persist_failed")
	require.NotNil(t, persistFailure, "missing state.persist_failed: %s", buf.String())
	require.Equal(t, "mark_unknown", persistFailure["step"])
	require.Equal(t, op.OperationID, persistFailure["operation_id"])
	require.NotEmpty(t, persistFailure["error"])
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", persistFailure["trace_id"])
}
