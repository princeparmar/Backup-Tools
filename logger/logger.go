package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger interface defines the logging methods
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	With(fields ...Field) Logger
	Sync() error
}

// loggerImpl implements the Logger interface
type loggerImpl struct {
	zapLogger *zap.Logger
}

// Global logger instance
var globalLogger Logger

// Initialize the global logger
func Init(logger *zap.Logger) {
	globalLogger = &loggerImpl{zapLogger: logger}
}

// NewRelicCore wraps a zapcore.Core with New Relic integration
type NewRelicCore struct {
	zapcore.Core
	Interceptor LogInterceptor
}

func (c *NewRelicCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *NewRelicCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	c.Interceptor.InterceptLogWithFields(ent, fields)
	return c.Core.Write(ent, fields)
}

func (c *NewRelicCore) With(fields []zapcore.Field) zapcore.Core {
	return &NewRelicCore{
		Core:        c.Core.With(fields),
		Interceptor: c.Interceptor,
	}
}

// LogInterceptor interface for New Relic integration
type LogInterceptor interface {
	InterceptLogWithFields(entry zapcore.Entry, fields []zapcore.Field)
}

// InitWithNewRelic initializes the logger with New Relic integration
func InitWithNewRelic(interceptor LogInterceptor) {
	// Create zap logger with New Relic integration
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = ISO8601TimeEncoder

	core := NewCore(
		NewJSONEncoder(config.EncoderConfig),
		AddSync(os.Stdout),
		zap.InfoLevel,
	)

	// Wrap core with New Relic interceptor
	newRelicCore := &NewRelicCore{
		Core:        core,
		Interceptor: interceptor,
	}

	zapLogger := zap.New(newRelicCore)

	// Initialize our wrapper logger
	Init(zapLogger)
	zap.ReplaceGlobals(zapLogger) // Keep this for any direct zap usage
}

// Get the global logger
func L() Logger {
	if globalLogger == nil {
		// Fallback to a basic logger if not initialized
		basicLogger, _ := zap.NewProduction()
		globalLogger = &loggerImpl{zapLogger: basicLogger}
	}
	return globalLogger
}

// Debug logs a debug message
func Debug(msg string, fields ...Field) {
	L().Debug(msg, fields...)
}

// Info logs an info message
func Info(msg string, fields ...Field) {
	L().Info(msg, fields...)
}

// Warn logs a warning message
func Warn(msg string, fields ...Field) {
	L().Warn(msg, fields...)
}

// Error logs an error message
func Error(msg string, fields ...Field) {
	L().Error(msg, fields...)
}

// Fatal logs a fatal message and exits
func Fatal(msg string, fields ...Field) {
	L().Fatal(msg, fields...)
}

// With creates a child logger with the specified fields
func With(fields ...Field) Logger {
	return L().With(fields...)
}

// Sync flushes any buffered log entries
func Sync() error {
	return L().Sync()
}

// Field creation functions - these wrap zap field functions
func String(key string, val string) Field {
	return zap.String(key, val)
}

func Int(key string, val int) Field {
	return zap.Int(key, val)
}

func Int64(key string, val int64) Field {
	return zap.Int64(key, val)
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

func (l *loggerImpl) Sync() error {
	return l.zapLogger.Sync()
}
