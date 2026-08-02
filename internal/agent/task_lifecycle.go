package agent

// todoCheckpointState is local to one ChatWithTools stream. It deliberately
// does not live in a package-global map, so cancellation and completion release
// the state together with the stream.
type todoCheckpointState uint8

const (
	todoCheckpointNone todoCheckpointState = iota
	todoCheckpointReview
	todoCheckpointReconcile
)

func (s todoCheckpointState) active() bool {
	return s != todoCheckpointNone
}

// advanceTodoCheckpoint applies the two successful checkpoint exits. A
// question answered during review does not unlock ordinary work; it moves to
// reconciliation, where only todo_write is available.
func advanceTodoCheckpoint(state todoCheckpointState, todoWriteSucceeded, questionAnswered bool) (todoCheckpointState, bool) {
	if !state.active() {
		return state, false
	}
	if todoWriteSucceeded {
		return todoCheckpointNone, true
	}
	if state == todoCheckpointReview && questionAnswered {
		return todoCheckpointReconcile, true
	}
	return state, false
}
