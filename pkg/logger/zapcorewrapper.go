package logger

import (
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
