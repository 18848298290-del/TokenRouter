package forward

// QoderErrorView holds display fields for one upstream error.
type QoderErrorView struct {
	Recognized           bool
	Status, SourceStatus int
	Kind, Message, Body  string
}
