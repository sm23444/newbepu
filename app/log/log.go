package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/natefinch/lumberjack"
	"github.com/sirupsen/logrus"
)

var (
	Task    *logrus.Logger
	be      *logrus.Logger
	loggers []*lumberjack.Logger
	logPath string
)

func newLogger(file string) (*logrus.Logger, error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(file, 0o600); err != nil {
		return nil, err
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		ForceColors:     true,
		ForceQuote:      true,
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	})

	logger.SetLevel(logrus.InfoLevel)
	output := &lumberjack.Logger{
		Filename:   file,
		MaxSize:    300,
		MaxBackups: 5,
		MaxAge:     7,
		Compress:   true,
	}

	logger.SetOutput(output)
	loggers = append(loggers, output)

	return logger, nil
}

func Init(dir string) error {
	if err := ensurePrivateDirectory(dir); err != nil {
		return fmt.Errorf("准备日志目录失败：%w", err)
	}

	var err error

	be, err = newLogger(filepath.Join(dir, "bepusdt.log"))
	if err != nil {
		return err
	}

	Task, err = newLogger(filepath.Join(dir, "task.log"))
	if err != nil {
		return err
	}

	logPath = dir

	return nil
}

func ensurePrivateDirectory(dir string) error {
	dir = filepath.Clean(dir)
	if dir == "." || filepath.Dir(dir) == dir {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func Debug(args ...interface{}) {
	be.Debugln(args...)
}

func Info(args ...interface{}) {
	be.Infoln(args...)
}

func Error(args ...interface{}) {
	be.Errorln(args...)
}

func Warn(args ...interface{}) {
	be.Warnln(args...)
}

func GetWriter() *io.PipeWriter {

	return be.Writer()
}

func GetPath() string {
	return logPath
}

func Close() {
	for _, f := range loggers {
		if f != nil {
			if err := f.Close(); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, fmt.Sprintf("日志句柄资源关闭错误：%s", err.Error()))
			}
		}
	}

	loggers = nil
}
