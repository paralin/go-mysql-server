package server

import "github.com/sirupsen/logrus"

// buildDefaultLogger builds the default logger if le is nil.
func buildDefaultLogger(le *logrus.Entry) *logrus.Entry {
	if le != nil {
		return le
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	return logrus.NewEntry(log)
}
