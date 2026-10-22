package loader

import (
	"errors"
	"testing"
)

func TestIssueLog_CapsReportedIssues(t *testing.T) {
	log := &issueLog{max: 2}
	for row := 1; row <= 5; row++ {
		log.add(row, errors.New("bad"))
	}

	if len(log.items) != 2 || log.dropped != 3 {
		t.Errorf("items = %d, dropped = %d, want 2 and 3", len(log.items), log.dropped)
	}
}

func TestIssueLog_PlainErrorHasNoColumn(t *testing.T) {
	log := &issueLog{max: 1}
	log.add(7, errors.New("something odd"))

	got := log.items[0]
	if got.Row != 7 || got.Column != "" || got.Message != "something odd" {
		t.Errorf("issue = %+v", got)
	}
}
