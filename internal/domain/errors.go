package domain

import "fmt"

type Problem struct {
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
}

func (p *Problem) Error() string {
	return fmt.Sprintf("%s: %s", p.Code, p.Message)
}

func NewProblem(code string, message string, retryable bool) *Problem {
	return &Problem{Code: code, Message: message, Retryable: retryable, Details: map[string]any{}}
}
