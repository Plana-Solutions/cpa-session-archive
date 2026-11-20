package chimport

import (
	"context"
	"errors"
	"strings"
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
		delay := 250 * time.Millisecond << attempt
		if strings.Contains(transient.Error(), `exception_code="241"`) || strings.Contains(transient.Error(), `exception_code="252"`) {
			delay = 5 * time.Second << min(attempt, 3)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
