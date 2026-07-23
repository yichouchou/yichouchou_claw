package memory

import (
	"fmt"
	"path/filepath"
	"time"
)

// localTimeZone is the project's canonical timezone (UTC+8, China Standard
// Time). Bucketing is intentionally local: traces in the middle of the night
// shouldn't spill into "yesterday" just because UTC has rolled over.
var localTimeZone = time.FixedZone("UTC+8", 8*3600)

// TimeBucket represents a (date, hour) bucket used to organize trace files.
// All values are in localTimeZone so a single bucket spans exactly one wall
// clock hour, regardless of DST or timezone arithmetic.
type TimeBucket struct {
	Year  int
	Month int
	Day   int
	Hour  int
}

// BucketFromTime converts an arbitrary wall-clock time to its bucket.
func BucketFromTime(t time.Time) TimeBucket {
	t = t.In(localTimeZone)
	return TimeBucket{
		Year:  t.Year(),
		Month: int(t.Month()),
		Day:   t.Day(),
		Hour:  t.Hour(),
	}
}

// BucketFromNow returns the bucket for the current wall clock time.
func BucketFromNow() TimeBucket {
	return BucketFromTime(time.Now())
}

// DateDir returns the day-level directory name, e.g. "2026-07-23".
func (b TimeBucket) DateDir() string {
	return fmt.Sprintf("%04d-%02d-%02d", b.Year, b.Month, b.Day)
}

// HourFile returns the file name for this hour bucket, e.g. "09h.md" or
// "14h.md". Hours are zero-padded so lexicographic order matches
// chronological order.
func (b TimeBucket) HourFile() string {
	return fmt.Sprintf("%02dh.md", b.Hour)
}

// BucketPath returns the full path of the hour bucket file under the
// supplied base directory. Layout: <base>/<YYYY-MM-DD>/<HH>h.md.
func (b TimeBucket) BucketPath(base string) string {
	return filepath.Join(base, b.DateDir(), b.HourFile())
}