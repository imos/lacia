package request

import "errors"

// RecordExternal stores a terminal result from a trusted coordinator. It
// never queues a conversation. The coordinator, not the executing device,
// supplies the request ID, source, prompt and reply destination.
// Repeating this call preserves the first result and reoffers only a reply
// with no delivery attempt. Delivery callbacks must use ClaimDelivery.
func (q *Queue) RecordExternal(r Request) error {
	if !idPattern.MatchString(r.ID) || r.Finished.IsZero() || r.Status != Done && r.Status != Failed && r.Status != Unknown {
		return errors.New("外部で終わった依頼のID、終了時刻、結果を指定してください")
	}
	q.mu.Lock()
	existing := q.items[r.ID]
	if existing != nil {
		if existing.Status == Queued || existing.Status == Running {
			q.mu.Unlock()
			return errors.New("このIDの依頼は、別の会話で進んでいます")
		}
		r = *existing
	} else {
		if r.Created.IsZero() {
			r.Created = q.now()
		}
		r.Kind = System
		r.Delivery = ""
		if err := q.save(&r); err != nil {
			q.mu.Unlock()
			return err
		}
		q.items[r.ID] = &r
	}
	notify := existing == nil || r.ReplyTo != "" && r.Delivery == ""
	q.mu.Unlock()
	if notify && q.Finished != nil {
		q.Finished(r)
	}
	return nil
}
