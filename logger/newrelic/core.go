package newrelic

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/logger"
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
	parsedEntry, jsonFields := li.parseLog(entry)

	logData := map[string]interface{}{
		"L": parsedEntry.Level.String(),
		"M": parsedEntry.Message,
		"C": parsedEntry.Caller.String(),
		"N": parsedEntry.LoggerName,
		"T": parsedEntry.Time.Format(time.RFC3339),
		"S": parsedEntry.Stack,
	}

	// Merge fields
	for k, v := range jsonFields {
		logData[k] = v
	}
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

// parseLog parses Storj's structured log format: LEVEL\tLOGGER\tCALLER\tMESSAGE\t{JSON}
func (li *LogInterceptor) parseLog(entry logger.Entry) (logger.Entry, map[string]interface{}) {
	parts := strings.Split(entry.Message, "\t")
	if len(parts) < 4 {
		return entry, make(map[string]interface{})
	}

	jsonFields := make(map[string]interface{})
	if len(parts) > 4 {
		json.Unmarshal([]byte(parts[4]), &jsonFields)
	}

	entryCaller := logger.NewEntryCaller(0, parts[2], 0, parts[2] != "")

	return logger.Entry{
		Level:      li.parseLevel(parts[0]),
		Time:       entry.Time,
		Message:    parts[3],
		Caller:     entryCaller,
		LoggerName: parts[1],
		Stack:      entry.Stack,
	}, jsonFields
}

func (li *LogInterceptor) parseLevel(levelStr string) logger.Level {
	switch strings.ToUpper(levelStr) {
	case "ERROR", "ERR":
		return logger.ErrorLevel
	case "WARN":
		return logger.WarnLevel
	case "DEBUG":
		return logger.DebugLevel
	default:
		return logger.InfoLevel
	}
}

// Close closes the interceptor
func (li *LogInterceptor) Close() {
	li.sender.Close()
}