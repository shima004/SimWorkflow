package operation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestTransferStreamingAndCancellation(t *testing.T) {
	var events []Event
	ctx, cancel := context.WithCancel(WithReporter(context.Background(), func(e Event) { events = append(events, e) }))
	reader := Reader(ctx, "Download", 6, bytes.NewBufferString("abcdef"))
	buffer := make([]byte, 3)
	if n, err := reader.Read(buffer); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if len(events) != 2 || events[1].Bytes != 3 || events[1].TotalBytes != 6 || events[0].TransferID == 0 || events[0].TransferID != events[1].TransferID {
		t.Fatal(events)
	}
	cancel()
	if n, err := reader.Read(buffer); n != 0 || !errors.Is(err, context.Canceled) || len(events) != 2 {
		t.Fatal(n, err, events)
	}
	var out bytes.Buffer
	writer := Writer(context.Background(), "Save", -1, &out)
	if _, err := io.Copy(writer, bytes.NewBufferString("unchanged")); err != nil || out.String() != "unchanged" {
		t.Fatal(err, out.String())
	}
}
