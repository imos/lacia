package codex

import "context"

// Rollover retires the current window once per night. The caller must have
// reserved the idle main request lane. Leave the app-server alive and release
// the old subscription without waiting on its reader. The next connection
// starts a new thread with current instructions.
func (b *Bridge) Rollover(night string, ready func() bool) (bool, error) {
	if b.cfg.Ephemeral || !b.setup.TryLock() {
		return false, nil
	}
	defer b.setup.Unlock()
	return b.rolloverLocked(night, ready)
}

// RolloverBeforeRequest can reconnect to the saved thread before any new
// turn begins. The main queue already owns the lane during this call.
func (b *Bridge) RolloverBeforeRequest(ctx context.Context, night string, ready func() bool) (bool, error) {
	if b.cfg.Ephemeral {
		return false, nil
	}
	b.setup.Lock()
	defer b.setup.Unlock()
	record, err := b.loadThread()
	if err != nil || record.ThreadID == "" || record.RolloverNight >= night || !ready() {
		return false, err
	}
	if err := b.connectLocked(ctx); err != nil {
		return false, err
	}
	return b.rolloverLocked(night, ready)
}

func (b *Bridge) rolloverLocked(night string, ready func() bool) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil || b.threadID == "" || !b.childStateKnown || b.childNotices != 0 {
		return false, nil
	}
	select {
	case <-b.client.closed:
		return false, nil // Let reconnection recover child state first.
	default:
	}
	for _, turn := range b.turns {
		if turn.status == "" {
			return false, nil
		}
	}
	b.routes.mu.Lock()
	defer b.routes.mu.Unlock()
	if err := b.loadRoutes(); err != nil {
		return false, err
	}
	for _, child := range b.routes.Children {
		if !child.Finished {
			return false, nil
		}
	}

	record, err := b.loadThread()
	if err != nil {
		return false, err
	}
	if record.ThreadID != b.threadID || record.RolloverNight >= night || (ready != nil && !ready()) {
		return false, nil
	}
	record.RolloverNight = night
	record.RetiredThreads = append(record.RetiredThreads, record.ThreadID)
	record.ThreadID, record.Rules = "", nil
	if err := b.writeThread(record); err != nil {
		return false, err
	}
	old := b.client
	b.client, b.threadID, b.ruleVersion = nil, "", nil
	b.turns = map[string]*turn{}
	go old.close()
	return true, nil
}
