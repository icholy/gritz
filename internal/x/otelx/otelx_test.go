package otelx

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"gotest.tools/v3/assert"
)

// collectAttrs records a value on a counter in the given instrumentation scope
// and returns the attribute sets of the resulting datapoints.
func collectAttrs(t *testing.T, scope string, attrs ...attribute.KeyValue) []attribute.Set {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(otelconnectPeerView),
	)
	counter, err := mp.Meter(scope).Int64Counter("rpc.server.duration")
	assert.NilError(t, err)
	counter.Add(context.Background(), 1, metric.WithAttributes(attrs...))

	var rm metricdata.ResourceMetrics
	assert.NilError(t, reader.Collect(context.Background(), &rm))
	assert.Equal(t, len(rm.ScopeMetrics), 1)
	assert.Equal(t, len(rm.ScopeMetrics[0].Metrics), 1)

	sum := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	sets := make([]attribute.Set, 0, len(sum.DataPoints))
	for _, dp := range sum.DataPoints {
		sets = append(sets, dp.Attributes)
	}
	return sets
}

func TestOtelconnectPeerViewDropsPeerAttributes(t *testing.T) {
	// Arrange & Act - two clients on different ephemeral ports
	sets := collectAttrs(t, otelconnectScope,
		attribute.String("rpc.method", "GetTask"),
		attribute.String("net.peer.name", "10.0.0.1"),
		attribute.Int("net.peer.port", 54321),
	)

	// Assert - the peer attributes are gone
	assert.Equal(t, len(sets), 1)
	assert.Equal(t, sets[0].Len(), 1)
	v, ok := sets[0].Value("rpc.method")
	assert.Assert(t, ok)
	assert.Equal(t, v.AsString(), "GetTask")
}

func TestOtelconnectPeerViewCollapsesStreams(t *testing.T) {
	// Arrange
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(otelconnectPeerView),
	)
	counter, err := mp.Meter(otelconnectScope).Int64Counter("rpc.server.duration")
	assert.NilError(t, err)

	// Act - the same RPC from many different client ports
	for port := 50000; port < 50010; port++ {
		counter.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("rpc.method", "GetTask"),
			attribute.Int("net.peer.port", port),
		))
	}

	// Assert - one stream, not ten
	var rm metricdata.ResourceMetrics
	assert.NilError(t, reader.Collect(context.Background(), &rm))
	sum := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	assert.Equal(t, len(sum.DataPoints), 1)
	assert.Equal(t, sum.DataPoints[0].Value, int64(10))
}

func TestOtelconnectPeerViewIgnoresOtherScopes(t *testing.T) {
	// Arrange & Act
	sets := collectAttrs(t, "some/other/scope",
		attribute.String("net.peer.name", "10.0.0.1"),
	)

	// Assert - other instrumentation keeps its attributes
	assert.Equal(t, len(sets), 1)
	_, ok := sets[0].Value("net.peer.name")
	assert.Assert(t, ok)
}
