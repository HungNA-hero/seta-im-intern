package consume

import "time"

type QuarantinedRecord struct {
	SourceTopic     string
	SourcePartition int
	SourceOffset    int64
	QuarantineID    string
	EventID         string
	AggregateID     string
	ObservedAt      time.Time
	ReasonCode      string
	PayloadSHA256   string
}
