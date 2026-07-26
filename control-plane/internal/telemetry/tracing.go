package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// InitTracing 初始化 OTLP HTTP 追踪(架构第 16 节)。
// endpoint 为空时返回 no-op TracerProvider(不导出,但代码仍可埋点)。
// 返回 shutdown 函数。
func InitTracing(ctx context.Context, serviceName, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if endpoint == "" {
		// no-op provider:Tracer() 可用但不导出
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return func(c context.Context) error {
		ctx2, cancel := context.WithTimeout(c, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx2)
	}, nil
}

// Tracer 返回命名 tracer(未初始化导出时为 no-op,安全)。
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// StringAttr 便捷属性。
func StringAttr(k, v string) attribute.KeyValue { return attribute.String(k, v) }
