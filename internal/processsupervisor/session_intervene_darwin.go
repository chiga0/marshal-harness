//go:build darwin

package processsupervisor

func (session *Session) intervene() {
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.state = sessionIntervention
}
