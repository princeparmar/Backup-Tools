package newrelic

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// LogInterceptor implements the logger.LogInterceptor interface
type LogInterceptor struct {
	sender *Sender
}

// NewLogInterceptor creates a new LogInterceptor
func NewLogInterceptor(apiKey string, enabled bool) *LogInterceptor {
	return &LogInterceptor{sender: NewSender(apiKey, enabled)}
}

// InterceptLogWithFields intercepts a log entry with structured fields and sends it to New Relic
func (li *LogInterceptor) InterceptLogWithFields(entry logger.Entry, fields []logger.Field) {
	fieldMap := li.fieldsToMap(fields)

	caller := entry.Caller.String()
	if caller == "" || caller == "undefined" {
		caller = entry.Caller.FullPath()
	}
	if caller == "" || caller == "undefined" {
		caller = "unknown"
	}

	logData := map[string]interface{}{
		"L": entry.Level.String(),
		"M": entry.Message,
		"C": caller,
		"N": entry.LoggerName,
		"T": entry.Time.Format(time.RFC3339),
		"S": entry.Stack,
	}

	// Merge fields directly from zap fields
	for k, v := range fieldMap {
		logData[k] = v
	}

	if jsonData, err := json.Marshal(logData); err == nil {
		li.sender.SendLog(jsonData)
	}
}

func (li *LogInterceptor) fieldsToMap(fields []logger.Field) map[string]interface{} {
	fieldMap := make(map[string]interface{})
	for _, field := range fields {
		switch field.Type {
		case logger.StringType:
			fieldMap[field.Key] = field.String
		case logger.Int64Type, logger.Int32Type, logger.Int16Type, logger.Int8Type,
			logger.Uint64Type, logger.Uint32Type, logger.Uint16Type, logger.Uint8Type:
			fieldMap[field.Key] = field.Integer
		case logger.Float64Type:
			fieldMap[field.Key] = field.Integer
		case logger.Float32Type:
			fieldMap[field.Key] = float32(field.Integer)
		case logger.BoolType:
			fieldMap[field.Key] = field.Integer == 1
		case logger.DurationType:
			fieldMap[field.Key] = time.Duration(field.Integer).String()
		case logger.TimeType:
			if field.Interface != nil {
				fieldMap[field.Key] = field.Interface.(time.Time)
			}
		case logger.ErrorType:
			if field.Interface != nil {
				fieldMap[field.Key] = field.Interface.(error).Error()
			}
		case logger.StringerType:
			if field.Interface != nil {
				fieldMap[field.Key] = field.Interface.(fmt.Stringer).String()
			}
		case logger.NamespaceType:
			// Skip namespace fields
		default:
			fieldMap[field.Key] = field.Interface
		}
	}
	return fieldMap
}

// Close closes the interceptor
func (li *LogInterceptor) Close() {
	li.sender.Close()
}
