package archiver

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"time"

	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/jmoiron/sqlx"
	"github.com/nyaruka/rp-archiver/s3"
	"github.com/sirupsen/logrus"
)

type ArchiveType string

const (
	FlowRunType = ArchiveType("flowrun")
	MessageType = ArchiveType("message")
	SessionType = ArchiveType("session")
)

type ArchivePeriod string

const (
	Day   = ArchivePeriod("D")
	Month = ArchivePeriod("M")
)

type Org struct {
	ID         int       `db:"id"`
	Name       string    `db:"name"`
	CreatedOn  time.Time `db:"created_on"`
	ActiveDays int
}

type Archive struct {
	ID          int       `db:"id"`
	ArchiveType string    `db:"archive_type"`
	OrgID       int       `db:"org_id"`
	CreatedOn   time.Time `db:"created_on"`

	StartDate time.Time `db:"start_date"`
	Period    string    `db:"period"`

	RecordCount int `db:"record_count"`

	ArchiveSize int64  `db:"archive_size"`
	ArchiveHash string `db:"archive_hash"`
	ArchiveURL  string `db:"archive_url"`

	IsPurged  bool `db:"is_purged"`
	BuildTime int  `db:"build_time"`

	Org         Org
	ArchiveFile string
}

const lookupActiveOrgs = `SELECT id, name, created_on FROM orgs_org WHERE is_active = TRUE order by id`

// GetActiveOrgs returns the active organizations sorted by id
func GetActiveOrgs(ctx context.Context, db *sqlx.DB) ([]Org, error) {
	rows, err := db.QueryxContext(ctx, lookupActiveOrgs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orgs := make([]Org, 0, 10)
	for rows.Next() {
		org := Org{ActiveDays: 90}
		err = rows.StructScan(&org)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}

	return orgs, nil
}

const lookupOrgArchives = `SELECT start_date, archive_type FROM archives_archive WHERE org_id = $1 AND archive_type = $2 AND period = 'D' ORDER BY start_date asc`

// GetArchiveTasks calculates what archives need to be generated for the passed in org this is calculated per day
func GetArchiveTasks(ctx context.Context, db *sqlx.DB, now time.Time, org Org, archiveType ArchiveType) ([]Archive, error) {
	archives := []Archive{}
	err := db.SelectContext(ctx, &archives, lookupOrgArchives, org.ID, archiveType)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	// our first archive would be active days from today
	endDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(time.Hour * 24 * -time.Duration(org.ActiveDays))
	orgUTC := org.CreatedOn.In(time.UTC)
	startDate := time.Date(orgUTC.Year(), orgUTC.Month(), orgUTC.Day(), 0, 0, 0, 0, time.UTC)

	tasks := make([]Archive, 0, 1)
	currentArchiveIdx := 0

	// walk forwards until we are after our end date
	for !startDate.After(endDate) {
		existing := false

		// advance our current archive idx until we are on our start date or later
		for currentArchiveIdx < len(archives) && archives[currentArchiveIdx].StartDate.Before(startDate) {
			currentArchiveIdx++
		}

		// do we already have this archive?
		if currentArchiveIdx < len(archives) && archives[currentArchiveIdx].StartDate.Equal(startDate) {
			existing = true
		}

		// this archive doesn't exist yet, we'll create it
		if !existing {
			archive := Archive{
				Org:         org,
				OrgID:       org.ID,
				StartDate:   startDate,
				ArchiveType: string(archiveType),
				Period:      string(Day),
			}
			tasks = append(tasks, archive)
		}

		startDate = startDate.Add(time.Hour * 24)
	}

	return tasks, nil
}

const lookupMsgs = `
select rec.visibility, row_to_json(rec) FROM (
	select
	  mm.id,
	  broadcast_id as broadcast,
	  row_to_json(contact) as contact,
	  CASE WHEN oo.is_anon = False THEN ccu.identity ELSE null END as urn,
	  row_to_json(channel) as channel,
	  CASE WHEN direction = 'I' THEN 'in'
		WHEN direction = 'O' THEN 'out'
		ELSE NULL
	  END as direction,
	  CASE WHEN msg_type = 'F'
		THEN 'flow'
	  WHEN msg_type = 'V'
		THEN 'ivr'
	  WHEN msg_type = 'I'
		THEN 'inbox'
	  ELSE NULL
	  END as "type",
	  CASE when status = 'I' then 'initializing'
		WHEN status = 'P' then 'queued'
		WHEN status = 'Q' then 'queued'
		WHEN status = 'W' then 'wired'
		WHEN status = 'D' then 'delivered'
		WHEN status = 'H' then 'handled'
		WHEN status = 'E' then 'errored'
		WHEN status = 'F' then 'failed'
		WHEN status = 'R' then 'resent'
		ELSE NULL
	  END as status,
	
	  CASE WHEN visibility = 'V' THEN 'visible'
		WHEN visibility = 'A' THEN 'archived'
		WHEN visibility = 'D' THEN 'deleted'
		ELSE NULL
		END as visibility,
	  text,
	  (select coalesce(jsonb_agg(attach_row), '[]'::jsonb) FROM (select attach_data.attachment[1] as content_type, attach_data.attachment[2] as url FROM (select regexp_matches(unnest(attachments), '^(.*?):(.*)$') attachment) as attach_data) as attach_row) as attachments,
	  labels_agg.data as labels,
	  mm.created_on,
	  sent_on
	from msgs_msg mm JOIN contacts_contacturn ccu ON mm.contact_urn_id = ccu.id JOIN orgs_org oo ON ccu.org_id = oo.id
	  JOIN LATERAL (select uuid, name from contacts_contact cc where cc.id = mm.contact_id) as contact ON True
	  JOIN LATERAL (select uuid, name from channels_channel ch where ch.id = mm.channel_id) as channel ON True
	  LEFT JOIN LATERAL (select coalesce(jsonb_agg(label_row), '[]'::jsonb) as data from (select uuid, name from msgs_label ml INNER JOIN msgs_msg_labels mml ON ml.id = mml.label_id AND mml.msg_id = mm.id) as label_row) as labels_agg ON True

	  WHERE mm.org_id = $1 AND mm.created_on >= $2 AND mm.created_on < $3
	order by created_on ASC, id ASC) rec; 
`

const lookupFlowRuns = `
select row_to_json(rec)
FROM (
   select
     fr.id,
     row_to_json(flow_struct) as flow,
     row_to_json(contact_struct) as contact,
     fr.responded,
     (select jsonb_agg(path_data) from (
          select path_row ->> 'node_uuid'  as node, path_row ->> 'arrived_on' as time
          from jsonb_array_elements(fr.path :: jsonb) as path_row) as path_data
     ) as path,
     (select jsonb_agg(values_data.tmp_values) from (
          select json_build_object(key, jsonb_build_object('name', value -> 'name', 'time', value -> 'created_on', 'category', value -> 'category', 'node', value -> 'node_uuid')) as tmp_values
          FROM jsonb_each(fr.results :: jsonb)) as values_data
     ) as values,
     fr.created_on,
     fr.modified_on,
     fr.exited_on,
     CASE
        WHEN exit_type = 'C'
          THEN 'completed'
        WHEN exit_type = 'I'
          THEN 'interrupted'
        WHEN exit_type = 'E'
          THEN 'expired'
        ELSE
          null
     END as exit_type

   FROM flows_flowrun fr
     JOIN LATERAL (SELECT uuid, name from flows_flow where flows_flow.id = fr.flow_id) as flow_struct ON True
     JOIN LATERAL (select uuid, name from contacts_contact cc where cc.id = fr.contact_id) as contact_struct ON True
   
   WHERE fr.org_id = $1 AND fr.created_on >= $2 AND fr.created_on < $3
   ORDER BY fr.created_on ASC, id ASC
) as rec;
`

// EnsureTempArchiveDirectory checks that we can write to our archive directory, creating it first if needbe
func EnsureTempArchiveDirectory(ctx context.Context, path string) error {
	if len(path) == 0 {
		return fmt.Errorf("path argument cannot be empty")
	}

	// check if path is a directory we can write to
	fileInfo, err := os.Stat(path)
	if os.IsNotExist(err) {
		return os.MkdirAll(path, 0700)
	} else if err != nil {
		return err
	}

	// is path a directory
	if !fileInfo.IsDir() {
		return fmt.Errorf("path '%s' is not a directory", path)
	}

	testFilePath := filepath.Join(path, ".test_file")
	testFile, err := os.Create(testFilePath)
	defer testFile.Close()

	if err != nil {
		return fmt.Errorf("directory '%s' is not writable", path)
	}

	err = os.Remove(testFilePath)
	return err
}

// CreateMsgArchive is responsible for writing an archive file for the passed in archive from our database
func CreateMsgArchive(ctx context.Context, db *sqlx.DB, archive *Archive, archivePath string) error {
	start := time.Now()

	log := logrus.WithFields(logrus.Fields{
		"org_id":       archive.Org.ID,
		"archive_type": archive.ArchiveType,
		"start_date":   archive.StartDate,
		"period":       archive.Period,
	})

	filename := fmt.Sprintf("%s_%d_%s%d%02d%02d", archive.ArchiveType, archive.Org.ID, archive.Period, archive.StartDate.Year(), archive.StartDate.Month(), archive.StartDate.Day())
	file, err := ioutil.TempFile(archivePath, filename)
	if err != nil {
		return err
	}
	hash := md5.New()
	gzWriter := gzip.NewWriter(io.MultiWriter(file, hash))
	writer := bufio.NewWriter(gzWriter)
	defer file.Close()

	log.WithField("filename", file.Name()).Debug("creating new archive file")

	endDate := archive.StartDate.Add(time.Hour * 24)
	rows, err := db.QueryxContext(ctx, lookupMsgs, archive.Org.ID, archive.StartDate, endDate)
	if err != nil {
		return err
	}
	defer rows.Close()

	recordCount := 0
	var msg, visibility string
	for rows.Next() {
		err = rows.Scan(&visibility, &msg)
		if err != nil {
			return err
		}

		// skip over deleted rows
		if visibility == "deleted" {
			continue
		}

		writer.WriteString(msg)
		writer.WriteString("\n")
		recordCount++

		if recordCount%100000 == 0 {
			log.WithField("filename", file.Name()).WithField("record_count", recordCount).WithField("elapsed", time.Now().Sub(start)).Debug("writing archive file")
		}
	}

	archive.ArchiveFile = file.Name()
	err = writer.Flush()
	if err != nil {
		return err
	}

	err = gzWriter.Close()
	if err != nil {
		return err
	}

	// calculate our size and hash
	archive.ArchiveHash = fmt.Sprintf("%x", hash.Sum(nil))
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	archive.ArchiveSize = stat.Size()
	archive.RecordCount = recordCount
	archive.BuildTime = int(time.Now().Sub(start) / time.Millisecond)

	log.WithFields(logrus.Fields{
		"record_count": recordCount,
		"filename":     file.Name(),
		"file_size":    archive.ArchiveSize,
		"file_hash":    archive.ArchiveHash,
		"elapsed":      time.Now().Sub(start),
	}).Debug("completed writing archive file")
	return nil
}

func UploadArchive(ctx context.Context, s3Client s3iface.S3API, bucket string, archive *Archive) error {
	// s3 wants a base64 encoded hash instead of our hex encoded
	hashBytes, _ := hex.DecodeString(archive.ArchiveHash)
	hashBase64 := base64.StdEncoding.EncodeToString(hashBytes)

	url, err := s3.PutS3File(
		s3Client,
		bucket,
		fmt.Sprintf("/%d/%s_%d_%02d_%s.jsonl.gz", archive.Org.ID, archive.ArchiveType, archive.StartDate.Year(), archive.StartDate.Month(), archive.ArchiveHash),
		"application/json",
		"gzip",
		archive.ArchiveFile,
		hashBase64,
	)

	if err == nil {
		archive.ArchiveURL = url
		logrus.WithFields(logrus.Fields{
			"org_id":       archive.Org.ID,
			"archive_type": archive.ArchiveType,
			"start_date":   archive.StartDate,
			"period":       archive.Period,
			"url":          archive.ArchiveURL,
			"file_size":    archive.ArchiveSize,
			"file_hash":    archive.ArchiveHash,
		}).Debug("completed uploading archive file")
	}
	return err
}

const insertArchive = `
INSERT INTO archives_archive(archive_type, org_id, created_on, start_date, period, record_count, archive_size, archive_hash, archive_url, is_purged, build_time)
              VALUES(:archive_type, :org_id, :created_on, :start_date, :period, :record_count, :archive_size, :archive_hash, :archive_url, :is_purged, :build_time)
RETURNING id
`

func WriteArchiveToDB(ctx context.Context, db *sqlx.DB, archive *Archive) error {
	archive.OrgID = archive.Org.ID
	archive.CreatedOn = time.Now()
	archive.IsPurged = false

	rows, err := db.NamedQueryContext(ctx, insertArchive, archive)
	if err != nil {
		return err
	}
	defer rows.Close()

	rows.Next()
	err = rows.Scan(&archive.ID)
	if err != nil {
		return err
	}

	return nil
}

// DeleteArchiveFile removes our own disk archive file
func DeleteArchiveFile(archive *Archive) error {
	err := os.Remove(archive.ArchiveFile)

	if err != nil {
		return err
	}

	log := logrus.WithFields(logrus.Fields{
		"org_id":        archive.Org.ID,
		"archive_type":  archive.ArchiveType,
		"start_date":    archive.StartDate,
		"periond":       archive.Period,
		"db_archive_id": archive.ID,
	})
	log.WithField("filename", archive.ArchiveFile).Debug("deleted temporary archive file")
	return nil
}
