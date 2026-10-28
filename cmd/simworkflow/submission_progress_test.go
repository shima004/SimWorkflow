package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"simworkflow/internal/operation"
)

func TestSubmissionProgressReportsStagesAndThrottlesCounts(t *testing.T) {
	var out bytes.Buffer
	report := submissionReporterWithClock(&out, func() time.Time { return time.Unix(1000, 0) })
	report(operation.Event{Stage: "maps", Total: 3})
	report(operation.Event{Stage: "maps", Completed: 1, Total: 3})
	report(operation.Event{Stage: "maps", Completed: 2, Total: 3})
	report(operation.Event{Stage: "maps", Completed: 3, Total: 3})
	report(operation.Event{Stage: "Worker configuration storage", Detail: "s3-sequence"})
	text := out.String()
	if strings.Count(text, "maps:") != 2 || !strings.Contains(text, "maps: 0/3") || !strings.Contains(text, "maps: 3/3") || !strings.Contains(text, "storage: s3-sequence") {
		t.Fatal("progress omitted stage boundaries or was not throttled", text)
	}
}
