package main

import (
	"context"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/evalphobia/logrus_sentry"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/nyaruka/ezconf"
	archiver "github.com/nyaruka/rp-archiver"
	"github.com/sirupsen/logrus"
)

func main() {
	config := archiver.NewConfig()
	loader := ezconf.NewLoader(&config, "archiver", "Archives RapidPro runs and msgs to S3", []string{"archiver.toml"})
	loader.MustLoad()

	if config.DeleteAfterUpload && !config.UploadToS3 {
		logrus.Fatal("cannot delete archives and also not upload to s3")
	}

	// configure our logger
	logrus.SetOutput(os.Stdout)
	logrus.SetFormatter(&logrus.TextFormatter{})

	level, err := logrus.ParseLevel(config.LogLevel)
	if err != nil {
		logrus.Fatalf("Invalid log level '%s'", level)
	}
	logrus.SetLevel(level)

	// if we have a DSN entry, try to initialize it
	if config.SentryDSN != "" {
		hook, err := logrus_sentry.NewSentryHook(config.SentryDSN, []logrus.Level{logrus.PanicLevel, logrus.FatalLevel, logrus.ErrorLevel})
		hook.Timeout = 0
		hook.StacktraceConfiguration.Enable = true
		hook.StacktraceConfiguration.Skip = 4
		hook.StacktraceConfiguration.Context = 5
		if err != nil {
			logrus.Fatalf("Invalid sentry DSN: '%s': %s", config.SentryDSN, err)
		}
		logrus.StandardLogger().Hooks.Add(hook)
	}

	db, err := sqlx.Open("postgres", config.DB)
	if err != nil {
		logrus.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	var s3Client s3iface.S3API
	if config.UploadToS3 {
		s3Client, err = archiver.NewS3Client(config)
		if err != nil {
			logrus.WithError(err).Fatal("unable to initialize s3 client")
		}
	}

	// ensure that we can actually write to the temp directory
	ctx := context.Background()
	err = archiver.EnsureTempArchiveDirectory(config.TempDir)
	if err != nil {
		logrus.WithError(err).Fatal("cannot write to temp directory")
	}

	// get our active orgs
	orgs, err := archiver.GetActiveOrgs(ctx, db)
	if err != nil {
		logrus.Fatal(err)
	}

	// for each org, do our export
	for _, org := range orgs {
		log := logrus.WithField("org", org.Name).WithField("org_id", org.ID)
		if config.ArchiveMessages {
			_, _, err = archiver.ArchiveOrg(ctx, time.Now(), config, db, s3Client, org, archiver.MessageType)
			if err != nil {
				log.WithError(err).Error()
			}
		}
		if config.ArchiveRuns {
			_, _, err = archiver.ArchiveOrg(ctx, time.Now(), config, db, s3Client, org, archiver.RunType)
			if err != nil {
				log.WithError(err).Error()
			}
		}
	}
}
