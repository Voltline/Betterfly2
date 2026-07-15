package kafkaconsumer

import "errors"

type ErrorClass string

const (
	ErrorClassPermanent ErrorClass = "permanent"
	ErrorClassDomain    ErrorClass = "domain"
	ErrorClassTransient ErrorClass = "transient"
)

type ClassifiedError struct {
	Class ErrorClass
	Err   error
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

func PermanentError(err error) error { return classified(ErrorClassPermanent, err) }
func DomainError(err error) error    { return classified(ErrorClassDomain, err) }
func TransientError(err error) error { return classified(ErrorClassTransient, err) }

func classified(class ErrorClass, err error) error {
	if err == nil {
		err = errors.New("unspecified processing failure")
	}
	return &ClassifiedError{Class: class, Err: err}
}

func ErrorClassOf(err error) ErrorClass {
	var classifiedErr *ClassifiedError
	if errors.As(err, &classifiedErr) {
		return classifiedErr.Class
	}
	return ErrorClassTransient
}

// ResultForError maps handler errors without weakening the default: unknown
// dependency failures remain retryable. Domain errors should normally have
// already produced a durable response and therefore not escape the handler.
func ResultForError(err error) Result {
	switch ErrorClassOf(err) {
	case ErrorClassPermanent, ErrorClassDomain:
		return Permanent(err)
	default:
		return Transient(err)
	}
}
