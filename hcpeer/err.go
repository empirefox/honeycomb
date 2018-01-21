package hcpeer

type AcceptError struct {
	Name string
}

func (e *AcceptError) Error() string {
	return "Fail to find peer: " + e.Name
}
