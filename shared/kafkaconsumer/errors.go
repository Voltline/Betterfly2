package kafkaconsumer

// ResultForError maps handler errors without weakening the default: unknown
// dependency failures remain retryable. Domain errors should normally have
// already produced a durable response and therefore not escape the handler.
func ResultForError(err error) Result {
	return Transient(err)
}
