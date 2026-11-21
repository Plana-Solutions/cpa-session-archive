package chimport

import (
	"fmt"
	"io"
	"time"
)

func recordProgress(log io.Writer, id int64, interval time.Duration) func() {
	if log == nil {
		return func() {}
	}
	start := time.Now()
	fmt.Fprintf(log, "record_id=%d phase=prepare\n", id)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(log, "record_id=%d phase=prepare elapsed=%s\n", id, time.Since(start).Round(time.Millisecond))
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		fmt.Fprintf(log, "record_id=%d phase=prepared elapsed=%s\n", id, time.Since(start).Round(time.Millisecond))
	}
}
