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

func TestOperationProgressTransferSizesUnknownTotalsAndTerminalSafety(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(1000, 0)
	report, closeProgress := operationReporter(&out, func() time.Time { return now })
	report(operation.Event{Stage: "Download\x1b[31m\nobject", TransferID: 1, TotalBytes: 2 << 20})
	report(operation.Event{Stage: "Download\x1b[31m\nobject", TransferID: 1, Bytes: 1 << 20, TotalBytes: 2 << 20})
	if strings.Contains(out.String(), "50%") {
		t.Fatal("transfer output was not throttled")
	}
	now = now.Add(time.Second)
	report(operation.Event{Stage: "Download\x1b[31m\nobject", TransferID: 1, Bytes: 1 << 20, TotalBytes: 2 << 20})
	report(operation.Event{Stage: "Download\x1b[31m\nobject", TransferID: 1, Bytes: 2 << 20, TotalBytes: 2 << 20})
	report(operation.Event{Stage: "Export image", TransferID: 2, Bytes: 123, TotalBytes: -1})
	closeProgress()
	text := out.String()
	for _, want := range []string{"50%", "1.0MiB/2.0MiB", "100%", "123B (size unknown)"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing transfer progress", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "Download\n") || strings.Count(text, "\n") != 4 {
		t.Fatal("unsafe or unexpected output", text)
	}
}
