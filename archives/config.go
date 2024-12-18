package archives

import "os"

// Config is our top level configuration object
type Config struct {
	DB        string `help:"the connection string for our database"`
	LogLevel  string `help:"the log level, one of error, warn, info, debug"`
	SentryDSN string `help:"the sentry configuration to log errors to, if any"`

	S3Endpoint       string `help:"the S3 endpoint we will write archives to"`
	S3Region         string `help:"the S3 region we will write archives to"`
	S3Bucket         string `help:"the S3 bucket we will write archives to"`
	S3DisableSSL     bool   `help:"whether we disable SSL when accessing S3. Should always be set to False unless you're hosting an S3 compatible service within a secure internal network"`
	S3ForcePathStyle bool   `help:"whether we force S3 path style. Should generally need to default to False unless you're hosting an S3 compatible service"`

	AWSAccessKeyID     string `help:"the access key id to use when authenticating S3"`
	AWSSecretAccessKey string `help:"the secret access key id to use when authenticating S3"`

	TempDir       string `help:"directory where temporary archive files are written"`
	KeepFiles     bool   `help:"whether we should keep local archive files after upload (default false)"`
	UploadToS3    bool   `help:"whether we should upload archive to S3"`
	CheckS3Hashes bool   `help:"whether to check S3 hashes of uploaded archives before deleting records"`

	ArchiveMessages bool   `help:"whether we should archive messages"`
	ArchiveRuns     bool   `help:"whether we should archive runs"`
	RetentionPeriod int    `help:"the number of days to keep before archiving"`
	Delete          bool   `help:"whether to delete messages and runs from the db after archival (default false)"`
	StartTime       string `help:"what time archive jobs should run in UTC HH:MM "`
	Once            bool   `help:"whether archiver should run once and exit (default false)"`

	LibratoUsername string `help:"the username that will be used to authenticate to Librato"`
	LibratoToken    string `help:"the token that will be used to authenticate to Librato"`
	InstanceName    string `help:"the unique name of this instance used for analytics"`

	RollupOrgTimeout          int `help:"rollup timeout for all org archives, limit in hours (default 3)"`
	BuildRollupArchiveTimeout int `help:"rollup for single archive timeout, limit in hours (default 1)"`
}

// NewDefaultConfig returns a new default configuration object
func NewDefaultConfig() *Config {
	hostname, _ := os.Hostname()

	config := Config{
		DB:       "postgres://localhost/archiver_test?sslmode=disable",
		LogLevel: "info",

		S3Endpoint:       "https://s3.amazonaws.com",
		S3Region:         "us-east-1",
		S3Bucket:         "dl-archiver-test",
		S3DisableSSL:     false,
		S3ForcePathStyle: false,

		AWSAccessKeyID:     "",
		AWSSecretAccessKey: "",

		TempDir:       "/tmp",
		KeepFiles:     false,
		UploadToS3:    true,
		CheckS3Hashes: true,

		ArchiveMessages: true,
		ArchiveRuns:     true,
		RetentionPeriod: 90,
		Delete:          false,
		StartTime:       "00:01",
		Once:            false,

		InstanceName: hostname,

		RollupOrgTimeout:          3,
		BuildRollupArchiveTimeout: 1,
	}

	return &config
}
