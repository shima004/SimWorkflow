package main

import (
	"fmt"
	"io"
	"time"

	"simworkflow/internal/operation"
)

func submissionReporter(out io.Writer) func(operation.Event) {
	return submissionReporterWithClock(out, time.Now)
}

func submissionReporterWithClock(out io.Writer, clock func() time.Time) func(operation.Event) {
	started := clock()
	lastStage := ""
	var lastPrinted time.Time
	return func(event operation.Event) {
		now := clock()
		if event.Stage == lastStage && event.Completed != event.Total && now.Sub(lastPrinted) < time.Second {
			return
		}
		lastStage, lastPrinted = event.Stage, now
		elapsed := now.Sub(started).Truncate(time.Second)
		if event.Detail != "" {
			fmt.Fprintf(out, "[%s] %s: %s\n", elapsed, event.Stage, event.Detail)
			return
		}
		fmt.Fprintf(out, "[%s] %s: %d/%d\n", elapsed, event.Stage, event.Completed, event.Total)
	}
}
