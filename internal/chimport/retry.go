package chimport

import (
	"context"
	"errors"
	"time"

	"github.com/mattn/go-sqlite3"
)

type transientError struct{ message string }

func (e transientError) Error() string { return e.message }

func sqliteBusy(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && (e.Code == sqlite3.ErrBusy || e.Code == sqlite3.ErrLocked)
}

func retry(ctx context.Context, f func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := f()
		var transient transientError
		if err == nil || attempt == 3 || (!sqliteBusy(err) && !errors.As(err, &transient)) {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
