package main

import (
	"crypto/md5"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/colinmarc/hdfs"
	"github.com/fluent/fluent-logger-golang/fluent"
	"github.com/sirupsen/logrus"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	conf = kingpin.Flag("conf", "TOML config file path.").Required().ExistingFile()
)

// fluentd Fluentd configuration
type fluentd struct {
	// Fluentd server hostname
	Host string

	// Fluentd server port
	Port int

	// Tag to use to post to Fluentd server
	Tag string
}

// config Main program config struct.
type config struct {
	// HDFS server hostname.
	Host string

	// Flag to indicate to use Fluentd logging
	UseFluentd bool

	// Fluentd configurations
	Fluentd fluentd
}

func formatTime(t time.Time) string {
	return t.Format(time.RFC3339)
}

func newMsgInfo(level logrus.Level, heading string, msg string, datetime time.Time) map[string]interface{} {
	return map[string]interface{}{
		"level":    levelToStr(level),
		"heading":  heading,
		"msg":      msg,
		"datetime": formatTime(datetime),
	}
}

func newFsLogInfo(level logrus.Level, heading string, fs hdfs.FsInfo, datetime time.Time) map[string]interface{} {
	// not using fs.Used because it does not take into account .Used from host
	return map[string]interface{}{
		"level":         levelToStr(level),
		"heading":       heading,
		"capacity":      fs.Capacity,
		"used":          fs.Capacity - fs.Remaining,
		"remaining":     fs.Remaining,
		"datetime":      formatTime(datetime),
		"usedProp":      float64(fs.Capacity-fs.Remaining) / float64(fs.Capacity),
		"remainingProp": float64(fs.Remaining) / float64(fs.Capacity),
	}
}

func levelToStr(level logrus.Level) string {
	switch level {
	case logrus.DebugLevel:
		return "debug"
	case logrus.InfoLevel:
		return "info"
	case logrus.WarnLevel:
		return "warning"
	case logrus.ErrorLevel:
		return "error"
	case logrus.FatalLevel:
		return "fatal"
	case logrus.PanicLevel:
		return "panic"
	}

	return "unknown"
}

func regularLog(level logrus.Level, heading string, msg string) {
	logrus.WithFields(newMsgInfo(level, heading, msg, time.Now())).Print()
}

func genFluentdLog(logger *fluent.Fluent, tag string) func(logrus.Level, string, string) {
	return func(level logrus.Level, heading string, msg string) {
		logger.Post(tag, newMsgInfo(level, heading, msg, time.Now()))
	}
}

func regularLogFs(level logrus.Level, heading string, fs hdfs.FsInfo) {
	logrus.WithFields(newFsLogInfo(level, heading, fs, time.Now())).Print()
}

func genFluentdLogFs(logger *fluent.Fluent, tag string) func(logrus.Level, string, hdfs.FsInfo) {
	return func(level logrus.Level, heading string, fs hdfs.FsInfo) {
		logger.Post(tag, newFsLogInfo(level, heading, fs, time.Now()))
	}
}

func genFluentdLogClose(logger *fluent.Fluent) func() {
	return func() {
		logger.Close()
	}
}

var log = regularLog
var logFs = regularLogFs
var logClose = func() {}

// Function literal type to take a HDFS src path, local dst path, and HDFS client
type pathAct func(string, string, *hdfs.Client, os.FileInfo)

func walkDir(dirname string, src string, dst string, client *hdfs.Client, act pathAct) error {
	srcDirPath := path.Join(src, dirname)
	dstDirPath := path.Join(dst, dirname)

	fileInfo, err := client.ReadDir(srcDirPath)

	if err != nil {
		return err
	}

	for _, f := range fileInfo {
		srcPath := path.Join(srcDirPath, f.Name())
		dstPath := path.Join(dstDirPath, f.Name())

		act(srcPath, dstPath, client, f)

		if f.IsDir() {
			walkDir(f.Name(), srcDirPath, dstDirPath, client, act)
		}
	}

	return nil
}

func isMatchingFilters(srcPath string, filters []*regexp.Regexp) bool {
	for _, r := range filters {
		if r.MatchString(srcPath) {
			return true
		}
	}

	return false
}

func isSimilarFile(srcPath string, dstPath string, client *hdfs.Client) (bool, error) {
	srcData, err := client.ReadFile(srcPath)

	if err != nil {
		return false, err
	}

	// allow for dst file not to exist
	dstData, err := ioutil.ReadFile(dstPath)

	if err != nil {
		return false, nil
	}

	return md5.Sum(srcData) == md5.Sum(dstData), nil
}

func exitOnErrMsg(heading string, errMsg string) {
	log(logrus.ErrorLevel, heading, errMsg)
	os.Exit(1)
}

func exitOnErr(heading string, err error) {
	if err != nil {
		exitOnErrMsg(heading, fmt.Sprintf("%v", err))
	}
}

func initLog(c config) error {
	if c.UseFluentd {
		logger, err := fluent.New(fluent.Config{
			FluentHost: c.Fluentd.Host,
			FluentPort: c.Fluentd.Port,
		})

		if err != nil {
			return err
		}

		log = genFluentdLog(logger, c.Fluentd.Tag)
		logFs = genFluentdLogFs(logger, c.Fluentd.Tag)
		logClose = genFluentdLogClose(logger)
	}

	log(logrus.InfoLevel, "INIT", "Log started")
	return nil
}

func main() {
	logrus.SetFormatter(&logrus.JSONFormatter{})
	kingpin.Parse()

	var c config
	_, err := toml.DecodeFile(*conf, &c)
	exitOnErr("INIT", err)

	err = initLog(c)
	exitOnErr("INIT", err)
	defer logClose()

	client, err := hdfs.New(c.Host)
	fs, err := client.StatFs()
	exitOnErr("HDFS", err)

	logFs(logrus.InfoLevel, "POLL", fs)
}
