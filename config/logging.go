package config

import (
	"errors"
	"strings"

	"maunium.net/go/maulogger/v2"
	as "maunium.net/go/mautrix/appservice"
)

type logging as.LogConfig

func (l *logging) validate() error {
	if l.Directory == "" {
		l.Directory = "./logs"
	}

	if l.FileNameFormat == "" {
		l.FileNameFormat = "{{.Date}}-{{.Index}}.log"
	}

	if l.FileDateFormat == "" {
		l.FileDateFormat = "2006-01-02"
	}

	if l.FileMode == 0 {
		l.FileMode = 384
	}

	if l.TimestampFormat == "" {
		l.TimestampFormat = "Jan _2, 2006 15:04:05"
	}

	if l.RawPrintLevel == "" {
		l.RawPrintLevel = "debug"
	} else {
		switch strings.ToUpper(l.RawPrintLevel) {
		case "TRACE":
			l.PrintLevel = -10
		case "DEBUG":
			l.PrintLevel = maulogger.LevelDebug.Severity
		case "INFO":
			l.PrintLevel = maulogger.LevelInfo.Severity
		case "WARN", "WARNING":
			l.PrintLevel = maulogger.LevelWarn.Severity
		case "ERR", "ERROR":
			l.PrintLevel = maulogger.LevelError.Severity
		case "FATAL":
			l.PrintLevel = maulogger.LevelFatal.Severity
		default:
			return errors.New("invalid print level " + l.RawPrintLevel)
		}
	}

	return nil
}

func (l *logging) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawLogging logging

	raw := rawLogging{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*l = logging(raw)

	return l.validate()
}

func (cfg *Config) CreateLogger() (maulogger.Logger, error) {
	logger := maulogger.Create()

	// create an as.LogConfig from our config so we can configure the logger
	realLogConfig := as.LogConfig(cfg.Logging)
	realLogConfig.Configure(logger)

	// Set the default logger.
	maulogger.DefaultLogger = logger.(*maulogger.BasicLogger)

	// If we were given a filename format attempt to open the file.
	if cfg.Logging.FileNameFormat != "" {
		if err := maulogger.OpenFile(); err != nil {
			return nil, err
		}
	}

	return logger, nil
}
