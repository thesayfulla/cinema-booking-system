package logger

import (
	"log"
	"os"
)

// Logger provides structured logging.
// In production, this would use a proper logging library like zap or logrus.
type Logger struct {
	*log.Logger
}

// NewLogger creates a new logger.
func NewLogger() *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile),
	}
}

// Info logs an info-level message.
func (l *Logger) Info(msg string, args ...any) {
	l.Printf(msg, args...)
}

// Error logs an error-level message.
func (l *Logger) Error(msg string, args ...any) {
	l.Printf("ERROR: "+msg, args...)
}

// Fatal logs a fatal error and exits.
func (l *Logger) Fatal(msg string, args ...any) {
	l.Fatalf(msg, args...)
}
