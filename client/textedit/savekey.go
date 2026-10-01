package textedit

// SaveQueueKey names the outbox.SaveQueue chain a text save belongs on: one
// document, one chain. The id that owns the bytes names it, never the row the
// edit was viewed through, because the flush paths hold different ids for one
// document and two chains would let them claim the same version concurrently.
// An empty content id falls back to the viewed row, as rpc.ContentID does,
// and never to the empty chain every document would share.
func SaveQueueKey(viewedID, contentID string) string {
	if contentID != "" {
		return contentID
	}
	return viewedID
}
