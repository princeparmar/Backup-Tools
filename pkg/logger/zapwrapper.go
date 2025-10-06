package logger

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Re-export zapcore types for use in other packages
type (
	Entry        = zapcore.Entry
	Field        = zapcore.Field
	Level        = zapcore.Level
	Core         = zapcore.Core
	CheckedEntry = zapcore.CheckedEntry
	EntryCaller  = zapcore.EntryCaller
)

// Re-export zapcore constants
const (
	DebugLevel = zapcore.DebugLevel
	InfoLevel  = zapcore.InfoLevel
	WarnLevel  = zapcore.WarnLevel
	ErrorLevel = zapcore.ErrorLevel
	FatalLevel = zapcore.FatalLevel
)

// Re-export zapcore field types
const (
	StringType    = zapcore.StringType
	Int64Type     = zapcore.Int64Type
	Int32Type     = zapcore.Int32Type
	Int16Type     = zapcore.Int16Type
	Int8Type      = zapcore.Int8Type
	Uint64Type    = zapcore.Uint64Type
	Uint32Type    = zapcore.Uint32Type
	Uint16Type    = zapcore.Uint16Type
	Uint8Type     = zapcore.Uint8Type
	Float64Type   = zapcore.Float64Type
	Float32Type   = zapcore.Float32Type
	BoolType      = zapcore.BoolType
	DurationType  = zapcore.DurationType
	TimeType      = zapcore.TimeType
	ErrorType     = zapcore.ErrorType
	StringerType  = zapcore.StringerType
	NamespaceType = zapcore.NamespaceType
)

// Re-export zapcore functions
var (
	NewEntryCaller     = zapcore.NewEntryCaller
	ISO8601TimeEncoder = zapcore.ISO8601TimeEncoder
	NewCore            = zapcore.NewCore
	NewJSONEncoder     = zapcore.NewJSONEncoder
	AddSync            = zapcore.AddSync
)

// Logger interface defines the logging methods
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	With(fields ...Field) Logger
	WithContext(ctx context.Context) Logger
	Sync() error
}

// loggerImpl implements the Logger interface
type loggerImpl struct {
	zapLogger *zap.Logger
}

// Global logger instance
var globalLogger Logger

// LogInterceptor interface for log interception (e.g., New Relic integration)
type LogInterceptor interface {
	InterceptLogWithFields(entry Entry, fields []Field)
}

// NewRelicCore wraps a zapcore.Core with log interception
type NewRelicCore struct {
	Core
	Interceptor LogInterceptor
}

// Check implements zapcore.Core interface
func (c *NewRelicCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

// Write implements zapcore.Core interface with interception
func (c *NewRelicCore) Write(ent Entry, fields []Field) error {
	// Intercept the log before writing
	if c.Interceptor != nil {
		c.Interceptor.InterceptLogWithFields(ent, fields)
	}
	return c.Core.Write(ent, fields)
}

// With implements zapcore.Core interface
func (c *NewRelicCore) With(fields []Field) Core {
	return &NewRelicCore{
		Core:        c.Core.With(fields),
		Interceptor: c.Interceptor,
	}
}

// Initialize the global logger
func Init(logger *zap.Logger) {
	globalLogger = &loggerImpl{zapLogger: logger}
}

// InitWithNewRelic initializes the logger with log interception
func InitWithNewRelic(interceptor LogInterceptor) {
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = ISO8601TimeEncoder
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder

	core := NewCore(
		NewJSONEncoder(config.EncoderConfig),
		AddSync(os.Stdout),
		zap.InfoLevel,
	)

	// Wrap core with interceptor
	interceptorCore := &NewRelicCore{
		Core:        core,
		Interceptor: interceptor,
	}

	zapLogger := zap.New(interceptorCore, zap.AddCaller(), zap.AddCallerSkip(2))
	Init(zapLogger)
	zap.ReplaceGlobals(zapLogger)
}

// InitDefault initializes a default logger without interception
func InitDefault() {
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = ISO8601TimeEncoder
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder

	zapLogger, err := config.Build(zap.AddCaller(), zap.AddCallerSkip(2))
	if err != nil {
		// Fallback to basic logger
		zapLogger = zap.NewExample()
	}

	Init(zapLogger)
	zap.ReplaceGlobals(zapLogger)
}

// Get the global logger
func L() Logger {
	if globalLogger == nil {
		// Initialize with default logger if not already initialized
		InitDefault()
	}
	return globalLogger
}

// Context key for trace ID
type contextKey string

const traceIDKey contextKey = "trace_id"

// WithTraceID adds trace ID to context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// GetTraceIDFromContext extracts trace ID from context
func GetTraceIDFromContext(ctx context.Context) (string, bool) {
	traceID, ok := ctx.Value(traceIDKey).(string)
	return traceID, ok
}

// Package-level convenience functions

func Debug(ctx context.Context, msg string, fields ...Field) {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	L().Debug(msg, fields...)
}

func Info(ctx context.Context, msg string, fields ...Field) {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	L().Info(msg, fields...)
}

func Warn(ctx context.Context, msg string, fields ...Field) {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	L().Warn(msg, fields...)
}

func Error(ctx context.Context, msg string, fields ...Field) {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	L().Error(msg, fields...)
}

func Fatal(ctx context.Context, msg string, fields ...Field) {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	L().Fatal(msg, fields...)
}

func With(fields ...Field) Logger {
	return L().With(fields...)
}

func WithContext(ctx context.Context) Logger {
	return L().WithContext(ctx)
}

func Sync() error {
	return L().Sync()
}

// Field creation functions
func String(key string, val string) Field {
	return zap.String(key, val)
}

func Int(key string, val int) Field {
	return zap.Int(key, val)
}

func Int64(key string, val int64) Field {
	return zap.Int64(key, val)
}

func Float64(key string, val float64) Field {
	return zap.Float64(key, val)
}

func Bool(key string, val bool) Field {
	return zap.Bool(key, val)
}

func Any(key string, val interface{}) Field {
	return zap.Any(key, val)
}

func ErrorField(err error) Field {
	return zap.Error(err)
}

func Duration(key string, val interface{}) Field {
	return zap.Duration(key, val.(interface{ Duration() time.Duration }).Duration())
}

// Implementation of Logger interface methods

func (l *loggerImpl) Debug(msg string, fields ...Field) {
	l.zapLogger.Debug(msg, fields...)
}

func (l *loggerImpl) Info(msg string, fields ...Field) {
	l.zapLogger.Info(msg, fields...)
}

func (l *loggerImpl) Warn(msg string, fields ...Field) {
	l.zapLogger.Warn(msg, fields...)
}

func (l *loggerImpl) Error(msg string, fields ...Field) {
	l.zapLogger.Error(msg, fields...)
}

func (l *loggerImpl) Fatal(msg string, fields ...Field) {
	l.zapLogger.Fatal(msg, fields...)
}

func (l *loggerImpl) With(fields ...Field) Logger {
	return &loggerImpl{zapLogger: l.zapLogger.With(fields...)}
}

func (l *loggerImpl) WithContext(ctx context.Context) Logger {
	if traceID, ok := GetTraceIDFromContext(ctx); ok {
		return &loggerImpl{zapLogger: l.zapLogger.With(zap.String("trace_id", traceID))}
	}
	return l
}

func (l *loggerImpl) Sync() error {
	return l.zapLogger.Sync()
}
